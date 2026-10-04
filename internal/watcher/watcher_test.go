package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitChange(t *testing.T, ch <-chan Change, want string, within time.Duration) Change {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case c := <-ch:
			if c.Path == want {
				return c
			}
		case <-deadline:
			t.Fatalf("did not observe change for %q", want)
		}
	}
}

func TestWatchesCreateModifyRemove(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New(Options{Workspace: dir, Roots: []string{"."}, Interval: 20 * time.Millisecond})
	ch := w.Run(ctx)
	<-w.Ready()

	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := waitChange(t, ch, "a.txt", time.Second)
	if c.Kind != KindChanged {
		t.Fatalf("want changed, got %v", c.Kind)
	}

	if err := os.WriteFile(f, []byte("22"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitChange(t, ch, "a.txt", time.Second)

	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	c = waitChange(t, ch, "a.txt", time.Second)
	if c.Kind != KindRemoved {
		t.Fatalf("want removed, got %v", c.Kind)
	}
}

func TestIncludeExclude(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New(Options{
		Workspace: dir,
		Roots:     []string{"."},
		Include:   []string{"*.go"},
		Exclude:   []string{"skip.go"},
		Interval:  20 * time.Millisecond,
	})
	ch := w.Run(ctx)
	<-w.Ready()

	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skip.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitChange(t, ch, "keep.go", time.Second)
	select {
	case c := <-ch:
		t.Fatalf("filtered change leaked: %+v", c)
	case <-time.After(200 * time.Millisecond):
	}
}
