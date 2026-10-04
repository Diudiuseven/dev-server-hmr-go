// Package store persists built artifacts with atomic replacement so
// readers never observe a partially written result.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ArtifactStore stores artifact blobs keyed by content address.
type ArtifactStore struct {
	dir string
	mu  sync.Mutex // serializes temp-file allocation only
	seq uint64
}

// NewArtifactStore creates a store backed by dir (created if needed).
func NewArtifactStore(dir string) (*ArtifactStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("artifact store: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("artifact store: create dir: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &ArtifactStore{dir: abs}, nil
}

// Key derives a filesystem-safe content key.
func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *ArtifactStore) path(key string) string {
	// split into shards to keep directories small
	safe := sanitize(key)
	if len(safe) >= 4 {
		return filepath.Join(s.dir, safe[:2], safe[2:4], safe)
	}
	return filepath.Join(s.dir, safe)
}

func sanitize(key string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, key)
}

// Put atomically stores data under the given key and returns the path
// from which it can be read. The destination file is either the previous
// complete content or the new complete content; partial writes are never
// visible. If the final file already exists with the same key it is left
// untouched (content-addressed).
func (s *ArtifactStore) Put(key string, data []byte) (string, error) {
	final := s.path(key)
	if _, err := os.Stat(final); err == nil {
		return final, nil // immutable content-addressed object
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", err
	}

	s.mu.Lock()
	s.seq++
	tmp := filepath.Join(s.dir, fmt.Sprintf(".tmp-%d-%d", os.Getpid(), s.seq))
	s.mu.Unlock()

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	// rename is atomic on POSIX within the same filesystem.
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	cleanup = false
	return final, nil
}

// Path returns the absolute path of a stored artifact, or "" if absent.
func (s *ArtifactStore) Path(key string) string {
	p := s.path(key)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// Get returns the artifact bytes for a key.
func (s *ArtifactStore) Get(key string) ([]byte, error) {
	p := s.path(key)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("artifact %s: %w", key, err)
	}
	return data, nil
}

// Has reports whether key is present.
func (s *ArtifactStore) Has(key string) bool {
	return s.Path(key) != ""
}
