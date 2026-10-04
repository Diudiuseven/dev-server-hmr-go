package coalescer

import (
	"context"
	"sort"
	"testing"
	"time"
)

func recv(t *testing.T, c *Coalescer[string], within time.Duration) []string {
	t.Helper()
	select {
	case got := <-c.Out():
		sort.Strings(got)
		return got
	case <-time.After(within):
		t.Fatal("timed out waiting for emission")
		return nil
	}
}

func TestBurstMergedIntoOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New[string](50 * time.Millisecond)
	go c.Run(ctx)

	for i := 0; i < 100; i++ {
		c.Signal("a")
		c.Signal("b")
		if i%10 == 0 {
			time.Sleep(2 * time.Millisecond) // still inside the window
		}
	}
	got := recv(t, c, time.Second)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected merged [a b], got %v", got)
	}
	// No second emission should follow.
	select {
	case extra := <-c.Out():
		t.Fatalf("unexpected extra emission %v", extra)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestLastSignalNotLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New[string](30 * time.Millisecond)
	go c.Run(ctx)

	// Drain first burst.
	c.Signal("v1")
	first := recv(t, c, time.Second)
	if len(first) != 1 || first[0] != "v1" {
		t.Fatalf("first = %v", first)
	}
	// A signal arriving just after emission is retained for the next one.
	c.Signal("v2")
	second := recv(t, c, time.Second)
	if len(second) != 1 || second[0] != "v2" {
		t.Fatalf("last signal lost: %v", second)
	}
}

func TestFlushDeliversImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New[string](time.Hour)
	go c.Run(ctx)
	c.Signal("x")
	c.Flush()
	got := recv(t, c, time.Second)
	if len(got) != 1 || got[0] != "x" {
		t.Fatalf("flush result = %v", got)
	}
}
