// Package events implements the lifecycle event bus.
//
// On Publish, an Event is persisted to the repository (durable, restart-safe)
// and a non-blocking in-process broadcast wakes any local subscribers
// (Pipeline workers, webhook fanout). External consumers can poll
// NextUnconsumedEvents directly.
package events

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

const defaultSubBuffer = 64

// Bus is the concrete publisher. It satisfies service.EventSink.
type Bus struct {
	repo      repository.Repository
	logger    *slog.Logger
	subBuffer int // per-subscriber channel buffer depth

	mu        sync.RWMutex
	subs      []chan repository.Event
	transport func(context.Context, repository.Event) error // optional cross-instance fan-out
	dropped   atomic.Int64                                  // events dropped due to subscriber backpressure
}

// Dropped returns the cumulative count of events dropped because a subscriber's
// buffer was full (the durable DB copy is unaffected).
func (b *Bus) Dropped() int64 { return b.dropped.Load() }

func New(repo repository.Repository, logger *slog.Logger) *Bus {
	return NewWithBuffer(repo, logger, defaultSubBuffer)
}

// NewWithBuffer creates a Bus where each subscriber channel is buffered to
// bufSize events. bufSize <= 0 falls back to defaultSubBuffer.
func NewWithBuffer(repo repository.Repository, logger *slog.Logger, bufSize int) *Bus {
	if logger == nil {
		logger = slog.Default()
	}
	if bufSize <= 0 {
		bufSize = defaultSubBuffer
	}
	return &Bus{repo: repo, logger: logger, subBuffer: bufSize}
}

// WithTransport attaches an optional cross-instance transport (e.g. Postgres
// LISTEN/NOTIFY). It is invoked for LOCAL-origin events only (from Publish), so
// remote events delivered via Deliver are never re-broadcast outward — that is
// what keeps a multi-replica cluster loop-free.
func (b *Bus) WithTransport(fn func(context.Context, repository.Event) error) *Bus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.transport = fn
	return b
}

// Publish persists the event then broadcasts it locally and, if a transport is
// attached, fans it out to other instances. Errors are logged but never
// propagated — lifecycle events must not break user requests.
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
	id, err := b.repo.InsertEvent(ctx, e)
	if err != nil {
		b.logger.Warn("event insert failed", "type", string(e.Type), "key", e.Key, "err", err)
		return
	}
	e.ID = id
	b.broadcast(e)
	b.mu.RLock()
	transport := b.transport
	b.mu.RUnlock()
	if transport != nil {
		if err := transport(ctx, e); err != nil {
			b.logger.Warn("event transport publish failed", "type", string(e.Type), "err", err)
		}
	}
}

// Deliver broadcasts an event that originated on ANOTHER instance to local
// subscribers only — it does not persist (the origin already did) and does not
// re-enter the transport (preventing a cross-replica notify storm). The
// transport listener calls this for each remote event.
func (b *Bus) Deliver(e repository.Event) {
	b.broadcast(e)
}

// Subscribe returns a channel of events for in-process consumers. The channel
// is buffered (64); if a consumer falls behind, events are dropped (the
// durable copy in the DB remains the source of truth).
// Callers MUST invoke the returned cancel func when they stop consuming to
// prevent goroutine leaks in the bus's subscriber list.
func (b *Bus) Subscribe() (<-chan repository.Event, func()) {
	ch := make(chan repository.Event, b.subBuffer)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch, func() { b.Unsubscribe(ch) }
}

// Unsubscribe removes a subscriber channel from the bus. Safe to call
// multiple times; subsequent calls are no-ops.
func (b *Bus) Unsubscribe(ch chan repository.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subs {
		if sub == ch {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

// Close shuts every subscriber channel (e.g. on graceful shutdown).
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		close(ch)
	}
	b.subs = nil
}

func (b *Bus) broadcast(e repository.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			// subscriber backpressure: drop, the DB has it
			b.dropped.Add(1)
			telemetry.IncEventDropped(context.Background())
		}
	}
}
