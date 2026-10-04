// Package hub fans engine events out to connected streaming clients and
// serves the current complete state to freshly connected clients.
package hub

import (
	"context"
	"sync"
)

// clientBuffer bounds events queued per client. Beyond this, the slowest
// consumer is skipped rather than blocking builds.
const clientBuffer = 64

// Client represents one connected event-stream consumer.
type Client struct {
	events chan []byte
	done   chan struct{}
}

// Events returns the event channel for this client.
func (c *Client) Events() <-chan []byte { return c.events }

// Done is closed when the client has been removed.
func (c *Client) Done() <-chan struct{} { return c.done }

// Hub tracks connected clients.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]struct{}
	// SnapshotFor is invoked on subscribe to produce the initial complete
	// state payload delivered to the new client.
	SnapshotFor func() []byte
}

// New creates a Hub.
func New() *Hub {
	return &Hub{clients: make(map[*Client]struct{})}
}

// Subscribe registers a client and returns it. The client immediately
// receives the current complete state (via Events) if a snapshot provider
// is configured. Cancelling ctx or calling the returned cancel function
// removes the client; removal never blocks any build or broadcast.
func (h *Hub) Subscribe(ctx context.Context) (*Client, func()) {
	c := &Client{
		events: make(chan []byte, clientBuffer),
		done:   make(chan struct{}),
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	var initial []byte
	if h.SnapshotFor != nil {
		initial = h.SnapshotFor()
	}
	if len(initial) > 0 {
		select {
		case c.events <- initial:
		default:
		}
	}

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.done)
			}
			h.mu.Unlock()
		})
	}
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return c, cancel
}

// Broadcast sends an event to every connected client. Clients whose
// buffer is full are skipped, so a stalled or disconnected client can
// never block event production or subsequent builds.
func (h *Hub) Broadcast(payload []byte) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		select {
		case c.events <- payload:
		default:
		}
	}
}

// Count returns the current number of connected clients.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Stop disconnects every client.
func (h *Hub) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
	h.clients = make(map[*Client]struct{})
}
