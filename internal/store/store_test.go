package store

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestPutGetContentAddressed(t *testing.T) {
	s, err := NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("artifact-"), 1024)
	p1, err := s.Put("k1", data)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := s.Put("k1", data)
	if p1 != p2 {
		t.Fatal("same key must resolve to same path")
	}
	got, err := s.Get("k1")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("artifact mismatch: err=%v len=%d", err, len(got))
	}
	if !s.Has("k1") || s.Has("missing") {
		t.Fatal("Has wrong")
	}
}

func TestNoTempFilesLeft(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewArtifactStore(dir)
	if _, err := s.Put("k", []byte("x")); err != nil {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if len(entries) != 0 {
		t.Fatalf("temp files leaked: %v", entries)
	}
}

// TestAtomicVisibility ensures a reader racing many writers of distinct
// versions only ever observes absent or fully written content.
func TestAtomicVisibility(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewArtifactStore(dir)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				select {
				case <-stop:
					return
				default:
				}
				key := Key(fmt.Sprintf("w%d", id), fmt.Sprintf("%d", j))
				payload := bytes.Repeat([]byte{byte('A' + id)}, 4096+j)
				if _, err := s.Put(key, payload); err != nil {
					t.Errorf("put: %v", err)
					return
				}
			}
		}(i)
	}
	go func() { wg.Wait(); close(stop) }()
loop:
	for {
		select {
		case <-stop:
			break loop
		default:
		}
		for _, key := range []string{"w0", "w1", "w2", "w3"} {
			for j := 0; j < 50; j++ {
				k := Key(key, fmt.Sprintf("%d", j))
				if s.Has(k) {
					b, err := s.Get(k)
					if err != nil {
						t.Fatalf("read failed: %v", err)
					}
					if len(b) == 0 || len(b) < 4096 {
						t.Fatalf("observed partial artifact len=%d for %s/%d", len(b), key, j)
					}
				}
			}
		}
	}
}
