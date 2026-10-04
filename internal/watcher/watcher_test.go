package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestWatcherReportsCreateAndModify(t *testing.T) {
	dir := t.TempDir()
	w := New([]string{dir}, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := w.Run(ctx)

	// Baseline: a pre-existing file must not be reported.
	pre := filepath.Join(dir, "pre.txt")
	if err := os.WriteFile(pre, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitChanges(t, ch, 300*time.Millisecond, func(paths []string) bool {
		return contains(paths, filepath.Join(dir, "new.txt"))
	})

	// Modify with a future timestamp so the metadata scan sees it.
	future := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(dir, "new.txt"), future, future)
	waitChanges(t, ch, 500*time.Millisecond, func(paths []string) bool {
		// At least one additional event for new.txt must arrive.
		return len(paths) > 0
	})
}

func TestWatcherSkipsTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	w := New([]string{dir}, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := w.Run(ctx)
	time.Sleep(40 * time.Millisecond) // baseline

	if err := os.WriteFile(filepath.Join(dir, "save.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "editor~"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-ch:
		t.Fatalf("temporary file produced an event: %+v", c)
	case <-time.After(120 * time.Millisecond):
	}
}

func TestWatcherStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	w := New([]string{dir}, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	ch := w.Run(ctx)
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel must be closed after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestWatcherRecursive(t *testing.T) {
	dir := t.TempDir()
	w := New([]string{dir}, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := w.Run(ctx)
	time.Sleep(40 * time.Millisecond)

	sub := filepath.Join(dir, "nested", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "x.go")
	if err := os.WriteFile(target, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitChanges(t, ch, 500*time.Millisecond, func(paths []string) bool {
		return contains(paths, target)
	})
}

func waitChanges(t *testing.T, ch <-chan Change, timeout time.Duration, cond func([]string) bool) {
	t.Helper()
	var got []string
	deadline := time.After(timeout)
	for {
		select {
		case c := <-ch:
			got = append(got, c.Path)
			if cond(got) {
				return
			}
		case <-deadline:
			t.Fatalf("expected change not observed; got %v", sort.StringSlice(got))
		}
	}
}

func contains(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}
