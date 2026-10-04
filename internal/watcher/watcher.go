// Package watcher detects source file changes under configurable roots
// using dependency-free filesystem polling.
package watcher

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Kind classifies a change.
type Kind int

const (
	// KindChanged covers creation and modification.
	KindChanged Kind = iota
	// KindRemoved is a deletion.
	KindRemoved
)

// Change is one detected filesystem change.
type Change struct {
	// Path is workspace-relative and slash separated.
	Path string
	Kind Kind
}

// Options configures a watcher.
type Options struct {
	// Workspace is the absolute workspace root.
	Workspace string
	// Roots are workspace-relative directories to scan.
	Roots []string
	// Include patterns (matched against the slash-separated relative path
	// using filepath.Match semantics on every path component; a pattern
	// containing "/" matches the whole path). Empty Include means all.
	Include []string
	// Exclude patterns override Include.
	Exclude []string
	// Interval between scans.
	Interval time.Duration
}

// Watcher polls the filesystem and reports changed files.
type Watcher struct {
	opts  Options
	ready chan struct{} // closed once the baseline scan has finished
}

// New creates a polling watcher.
func New(opts Options) *Watcher {
	if opts.Interval <= 0 {
		opts.Interval = 100 * time.Millisecond
	}
	return &Watcher{opts: opts, ready: make(chan struct{})}
}

// Ready is closed after the initial baseline scan completes. Changes made
// after Ready is closed are guaranteed to be reported.
func (w *Watcher) Ready() <-chan struct{} { return w.ready }

// signature identifies a file version cheaply.
type signature struct {
	mtime time.Time
	size  int64
}

// Run scans until ctx is cancelled, sending changed paths on the returned
// channel. The first scan only establishes a baseline and emits nothing.
func (w *Watcher) Run(ctx context.Context) <-chan Change {
	out := make(chan Change, 256)
	go func() {
		defer close(out)
		known := map[string]signature{}
		if !w.scan(ctx, known, nil) {
			return // cancelled during baseline
		}
		close(w.ready)
		t := time.NewTicker(w.opts.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !w.scan(ctx, known, out) {
					return
				}
			}
		}
	}()
	return out
}

// scan walks the roots once, updating known and emitting deltas to out
// (nil out means baseline mode). It returns false if ctx was cancelled.
func (w *Watcher) scan(ctx context.Context, known map[string]signature, out chan<- Change) bool {
	seen := make(map[string]bool, len(known))
	var changes []Change
	roots := w.opts.Roots
	if len(roots) == 0 {
		roots = []string{"."}
	}
	for _, root := range roots {
		absRoot := filepath.Join(w.opts.Workspace, root)
		err := filepath.WalkDir(absRoot, func(abs string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // tolerate transient filesystem errors
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if d.IsDir() {
				base := d.Name()
				if base != "." && (base == ".git" || strings.HasPrefix(base, ".")) && abs != absRoot {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(w.opts.Workspace, abs)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if !w.accepted(rel) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			sig := signature{mtime: info.ModTime(), size: info.Size()}
			seen[rel] = true
			if prev, ok := known[rel]; !ok {
				if out != nil {
					changes = append(changes, Change{Path: rel, Kind: KindChanged})
				}
			} else if prev != sig {
				if out != nil {
					changes = append(changes, Change{Path: rel, Kind: KindChanged})
				}
			}
			known[rel] = sig
			return nil
		})
		if err != nil {
			return false
		}
	}
	if out != nil {
		for rel := range known {
			if !seen[rel] {
				changes = append(changes, Change{Path: rel, Kind: KindRemoved})
				delete(known, rel)
			}
		}
		sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
		for _, ch := range changes {
			select {
			case out <- ch:
			case <-ctx.Done():
				return false
			}
		}
	}
	return true
}

// accepted applies include/exclude glob rules.
func (w *Watcher) accepted(rel string) bool {
	matchAny := func(patterns []string) bool {
		for _, p := range patterns {
			if ok, _ := filepath.Match(p, rel); ok {
				return true
			}
			// also allow basename-only patterns
			if ok, _ := filepath.Match(p, filepath.Base(rel)); ok {
				return true
			}
		}
		return false
	}
	if len(w.opts.Exclude) > 0 && matchAny(w.opts.Exclude) {
		return false
	}
	if len(w.opts.Include) > 0 && !matchAny(w.opts.Include) {
		return false
	}
	return true
}
