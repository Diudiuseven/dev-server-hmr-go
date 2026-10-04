// Package coalescer merges a burst of change notifications into one
// trigger, while guaranteeing the last notification is never lost.
package coalescer

import (
	"context"
	"sync"
	"time"
)

// Coalescer debounces signals carrying comparable payloads. Payloads are
// deduplicated: a burst of signals within the merge window results in a
// single emission carrying the union of all payloads. A signal arriving
// while the consumer is busy is retained and emitted afterwards.
type Coalescer[T comparable] struct {
	window time.Duration

	mu      sync.Mutex
	pending map[T]struct{}

	kick chan struct{} // 1-buffered activity signal
	now  chan struct{} // 1-buffered force-flush signal
	out  chan []T
}

// New creates a Coalescer with the given merge window.
func New[T comparable](window time.Duration) *Coalescer[T] {
	if window < 0 {
		window = 0
	}
	return &Coalescer[T]{
		window:  window,
		pending: make(map[T]struct{}),
		kick:    make(chan struct{}, 1),
		now:     make(chan struct{}, 1),
		out:     make(chan []T, 64),
	}
}

// Signal records a payload. Safe for concurrent use; never blocks.
func (c *Coalescer[T]) Signal(v T) {
	c.mu.Lock()
	c.pending[v] = struct{}{}
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Out returns the emission channel.
func (c *Coalescer[T]) Out() <-chan []T { return c.out }

// Flush requests immediate emission of any pending payloads.
func (c *Coalescer[T]) Flush() {
	select {
	case c.now <- struct{}{}:
	default:
	}
}

func (c *Coalescer[T]) pendingLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// drain removes and returns pending payloads, or nil if empty.
func (c *Coalescer[T]) drain() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return nil
	}
	items := make([]T, 0, len(c.pending))
	for v := range c.pending {
		items = append(items, v)
	}
	c.pending = make(map[T]struct{})
	return items
}

// Run drives coalescing until ctx is cancelled.
//
// Algorithm (trailing-edge debounce):
//  1. Block until a kick (payload exists).
//  2. Start the window timer. Whenever a kick or flush arrives during the
//     window, drain pending first, reset the timer and continue waiting.
//  3. When the timer elapses (quiet for a full window) or Flush is hit,
//     emit whatever is pending.
//
// Buffered/residual signals are defused by checking pending state at each
// branch rather than trusting the signal channel alone.
func (c *Coalescer[T]) Run(ctx context.Context) {
	for {
		// Idle: wait for activity. A stale buffered now is ignored here.
		select {
		case <-ctx.Done():
			return
		case <-c.kick:
		}
		// Defuse any kicks that were already buffered; pending holds truth.
		c.drainSignal(c.kick)

		timer := time.NewTimer(c.window)
	window:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-c.kick:
				// Drain ALL buffered kick tokens first, THEN inspect pending.
				// A Signal() racing with this branch either finished writing
				// pending before its (now drained) token was sent, or sends
				// a fresh token after the drain — both are observable.
				c.drainSignal(c.kick)
				if c.pendingLen() > 0 {
					resetTimer(timer, c.window)
					continue
				}
				// Token without payload (e.g. retry hint with empty pending):
				// just continue waiting for the current window to elapse.
			case <-c.now:
				if items := c.drain(); items != nil {
					c.emit(ctx, items)
					break window
				}
				// No payload: ignore, keep waiting in the window.
			case <-timer.C:
				if items := c.drain(); items != nil {
					c.emit(ctx, items)
				}
				// Empty expiry (payload raced away) or a successful emit:
				// always return to idle. Empty payloads are never emitted, so
				// consumers only ever wake for real batches.
				break window
			}
		}
	}
}

func (c *Coalescer[T]) drainSignal(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// emit delivers items without blocking. If the out channel is full the
// payload is restored into pending and a kick scheduled so nothing is lost.
func (c *Coalescer[T]) emit(ctx context.Context, items []T) {
	select {
	case c.out <- items:
		return
	default:
	}
	c.mu.Lock()
	for _, v := range items {
		c.pending[v] = struct{}{}
	}
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
	// Give the consumer a chance to read, then keep looping via the
	// window logic using a short retry timer.
	select {
	case <-ctx.Done():
	case <-time.After(c.window):
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
