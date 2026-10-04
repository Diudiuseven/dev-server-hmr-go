package hub

import (
	"context"
	"testing"
	"time"
)

func TestSubscribeGetsSnapshotAndBroadcasts(t *testing.T) {
	h := New()
	h.SnapshotFor = func() []byte { return []byte("SNAP") }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, unsub := h.Subscribe(ctx)
	defer unsub()
	if got := <-c.Events(); string(got) != "SNAP" {
		t.Fatalf("initial payload = %q", got)
	}
	h.Broadcast([]byte("e1"))
	select {
	case got := <-c.Events():
		if string(got) != "e1" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive broadcast")
	}
}

func TestSlowClientNeverBlocks(t *testing.T) {
	h := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _ := h.Subscribe(ctx)
	// drain nothing: buffer fills up
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.Broadcast([]byte("x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on slow client")
	}
	_ = c
}

func TestDisconnectRemovesClient(t *testing.T) {
	h := New()
	ctx, cancel := context.WithCancel(context.Background())
	c, unsub := h.Subscribe(ctx)
	if h.Count() != 1 {
		t.Fatal("client not registered")
	}
	unsub()
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("done not closed")
	}
	if h.Count() != 0 {
		t.Fatal("client not removed")
	}
	cancel()
}
