package hub

import (
	"sync"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
)

func snap(epoch uint64) *artifact.Snapshot {
	states := map[string]artifact.State{
		"a": {Name: "a", Version: "v", Status: artifact.StatusOK},
	}
	return artifact.NewSnapshot(epoch, states, nil)
}

func TestNewSubscriberGetsCurrentSnapshot(t *testing.T) {
	h := New()
	h.Publish(snap(7))
	ch, cancel := h.Subscribe()
	defer cancel()
	select {
	case got := <-ch:
		if got.Epoch != 7 {
			t.Fatalf("new client got epoch %d, want current 7", got.Epoch)
		}
	case <-time.After(time.Second):
		t.Fatal("new client did not immediately receive the current snapshot")
	}
}

func TestBroadcastToConcurrentClients(t *testing.T) {
	h := New()
	const n = 20
	var chs []<-chan *artifact.Snapshot
	for i := 0; i < n; i++ {
		ch, _ := h.Subscribe() // no snapshot exists yet
		chs = append(chs, ch)
	}
	h.Publish(snap(1))
	h.Publish(snap(2))
	var wg sync.WaitGroup
	for i, ch := range chs {
		wg.Add(1)
		go func(i int, ch <-chan *artifact.Snapshot) {
			defer wg.Done()
			want := []uint64{1, 2}
			for j := 0; j < len(want); j++ {
				select {
				case got, ok := <-ch:
					if !ok {
						t.Errorf("client %d channel closed early", i)
						return
					}
					if got.Epoch != want[j] {
						t.Errorf("client %d got epoch %d, want %d", i, got.Epoch, want[j])
						return
					}
				case <-time.After(time.Second):
					t.Errorf("client %d missed update %d", i, want[j])
					return
				}
			}
		}(i, ch)
	}
	wg.Wait()
}

func TestSlowClientDroppedNotBlocking(t *testing.T) {
	h := New()
	ch, _ := h.Subscribe()
	// Saturate its buffer without reading.
	for i := 0; i < 100; i++ {
		h.Publish(snap(uint64(i + 1)))
	}
	if h.Len() != 0 {
		t.Fatalf("slow client must be dropped, hub len = %d", h.Len())
	}
	select {
	case _, ok := <-ch:
		if ok {
			// drain closed check below
		}
	default:
	}
	// Publishing with no clients must return promptly and never block.
	done := make(chan struct{})
	go func() { h.Publish(snap(101)); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on disconnected/slow clients")
	}
}

func TestDisconnectedClientDoesNotBlock(t *testing.T) {
	h := New()
	_, cancel := h.Subscribe()
	cancel()
	h.Publish(snap(1))
	if h.Len() != 0 {
		t.Fatal("unsubscribed client still counted")
	}
}

func TestCloseShutsDownClients(t *testing.T) {
	h := New()
	ch, _ := h.Subscribe()
	h.Close()
	if _, ok := <-ch; ok {
		t.Fatal("client channel should be closed on hub shutdown")
	}
	// Publish after close is a no-op and must not panic.
	h.Publish(snap(1))
}
