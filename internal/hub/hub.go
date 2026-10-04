package hub

import (
	"sync"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
)

// clientBuffer bounds the number of undelivered snapshots a slow client
// may queue. Overflowing clients are disconnected instead of slowing
// down builds or blocking other clients.
const clientBuffer = 16

type client struct {
	ch     chan *artifact.Snapshot
	closed bool
}

// Hub broadcasts snapshots to connected clients. Slow or disconnected
// clients are dropped and never block builds or other clients.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
	latest  *artifact.Snapshot
	closed  bool
}

// New creates an empty Hub.
func New() *Hub {
	return &Hub{clients: map[*client]struct{}{}}
}

// Subscribe registers a client; it immediately receives the latest
// snapshot if one exists, so a newly connected client gets the current
// complete version and status without waiting for a change.
// The returned channel is closed when the server shuts down or the
// client falls too far behind; call the returned cancel function when
// the client disconnects.
func (h *Hub) Subscribe() (<-chan *artifact.Snapshot, func()) {
	c := &client{ch: make(chan *artifact.Snapshot, clientBuffer)}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(c.ch)
		return c.ch, func() {}
	}
	h.clients[c] = struct{}{}
	latest := h.latest
	h.mu.Unlock()
	if latest != nil {
		c.ch <- latest
	}
	cancel := func() { h.remove(c) }
	return c.ch, cancel
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	if !c.closed {
		c.closed = true
		close(c.ch)
	}
}

// Publish stores the snapshot as the latest state and sends it to every
// connected client without blocking: a client whose buffer is full is
// dropped entirely (it must re-subscribe to catch up).
func (h *Hub) Publish(snap *artifact.Snapshot) {
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		return
	}
	h.mu.RUnlock()

	h.mu.Lock()
	h.latest = snap
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()

	for _, c := range clients {
		select {
		case c.ch <- snap:
		default:
			// Slow consumer: drop it rather than block the build loop.
			h.remove(c)
		}
	}
}

// Len returns the number of connected clients.
func (h *Hub) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Close shuts down all client channels; after Close, Publish is a no-op.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for c := range h.clients {
		if !c.closed {
			c.closed = true
			close(c.ch)
		}
		delete(h.clients, c)
	}
}
