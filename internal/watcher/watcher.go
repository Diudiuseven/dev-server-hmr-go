package watcher

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Change is one filesystem change event.
type Change struct {
	Path string
}

// Watcher polls the watched directories recursively and reports changed
// file paths. Polling keeps the implementation dependency-free and makes
// notifications de-duplicable by content; the orchestrator merges the
// resulting burst within its debounce window.
type Watcher struct {
	dirs     []string
	interval time.Duration
}

// New creates a Watcher for the given absolute directories with the
// given poll interval.
func New(dirs []string, interval time.Duration) *Watcher {
	cp := append([]string(nil), dirs...)
	return &Watcher{dirs: cp, interval: interval}
}

type fileInfo struct {
	modTime int64
	size    int64
	mode    os.FileMode
}

// Run emits changed (created or modified) regular-file paths on the
// returned channel until ctx is canceled. The first scan establishes a
// baseline without emitting, which matches "notify on save after start".
func (w *Watcher) Run(ctx context.Context) <-chan Change {
	out := make(chan Change, 256)
	go func() {
		defer close(out)
		known := map[string]fileInfo{}
		w.scan(known, nil) // baseline
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var batch []Change
				w.scan(known, func(p string, fi fs.FileInfo) {
					batch = append(batch, Change{Path: p})
				})
				for _, ch := range batch {
					select {
					case out <- ch:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out
}

// scan walks every watched directory, updating known and calling changed
// for files that differ from the recorded metadata.
func (w *Watcher) scan(known map[string]fileInfo, changed func(string, fs.FileInfo)) {
	for _, dir := range w.dirs {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // directories may appear/disappear mid-walk
			}
			name := d.Name()
			if d.IsDir() {
				if name == ".git" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || isTemporary(name) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			cur := fileInfo{modTime: info.ModTime().UnixNano(), size: info.Size(), mode: info.Mode().Perm()}
			prev, seen := known[p]
			known[p] = cur
			if changed != nil && (!seen || prev != cur) {
				changed(p, info)
			}
			return nil
		})
	}
}

// isTemporary filters editor swap/save artifacts so an atomic save does
// not produce noise notifications (the final rename is still caught).
func isTemporary(name string) bool {
	switch {
	case strings.HasSuffix(name, "~"),
		strings.HasSuffix(name, ".swp"),
		strings.HasSuffix(name, ".tmp"),
		strings.HasPrefix(name, ".#"),
		strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#"):
		return true
	}
	return false
}
