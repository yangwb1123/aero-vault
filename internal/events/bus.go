// Package events implements the lifecycle event bus.
//
// On Publish, an Event is persisted to the repository (durable, restart-safe)
// and a non-blocking in-process broadcast wakes any local subscribers
// (Pipeline workers, webhook fanout). External consumers can poll
// NextUnconsumedEvents directly.
package events

import (
	"context"
	"errors"
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

	mu                  sync.RWMutex
	subs                []chan repository.Event
	transport           func(context.Context, repository.Event) error // optional cross-instance fan-out
	boundary            *ResourceBoundary                             // transport and remote-delivery admission
	boundaryConfigured  bool
	authority           *EventAuthority // authoritative remote delivery
	authorityConfigured bool
	dropped             atomic.Int64 // events dropped due to subscriber backpressure
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
// remote events delivered via DeliverContext are never re-broadcast outward —
// that is what keeps a multi-replica cluster loop-free. Transport egress is
// additionally admitted against the exact event tenant/bucket.
func (b *Bus) WithTransport(fn func(context.Context, repository.Event) error) *Bus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.transport = fn
	return b
}

// WithResourceBoundary injects the event-local deny-only boundary. An explicit
// nil is a configuration fault: it blocks transport and remote delivery rather
// than becoming a legacy allow.
func (b *Bus) WithResourceBoundary(boundary *ResourceBoundary) *Bus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.boundary, b.boundaryConfigured = boundary, true
	return b
}

// WithEventAuthority injects the exact persisted-event reader used by remote
// delivery. An explicit nil is a fault; DeliverContext stops broadcasting until
// a usable authority is installed.
func (b *Bus) WithEventAuthority(authority *EventAuthority) *Bus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.authority, b.authorityConfigured = authority, true
	return b
}

// WithRepository replaces the persistence adapter before traffic is served.
// It is used by optional durable wrappers that must atomically extend event
// persistence without changing FileService or protocol adapters.
func (b *Bus) WithRepository(repo repository.Repository) *Bus {
	if repo == nil {
		return b
	}
	b.mu.Lock()
	b.repo = repo
	b.mu.Unlock()
	return b
}

// Publish persists the event then broadcasts it locally and, if a transport is
// attached, fans it out to other instances. Boundary refusal affects only
// transport egress, never persistence or local broadcast. Errors are logged but
// never propagated — lifecycle events must not break user requests.
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
	b.mu.RLock()
	repo := b.repo
	b.mu.RUnlock()
	id, err := repo.InsertEvent(ctx, e)
	if err != nil {
		b.logger.Warn("event insert failed", "type", string(e.Type), "error_class", "persistence")
		return
	}
	e.ID = id
	b.broadcast(e)
	b.publishTransport(ctx, e)
}

func (b *Bus) publishTransport(ctx context.Context, e repository.Event) {
	b.mu.RLock()
	transport := b.transport
	boundary, configured := b.boundary, b.boundaryConfigured
	b.mu.RUnlock()
	if transport == nil {
		return
	}
	// Unscoped legacy wakeups carry no resource identity to classify. They keep
	// the historical path only when no boundary was explicitly configured; every
	// scoped or object-bearing event is admitted, and a missing boundary is a
	// fault rather than an allow.
	if !configured && unscopedEvent(e) {
		b.sendTransport(ctx, transport, e)
		return
	}
	if err := admitEventBucket(ctx, boundary, e.TenantID, e.Bucket); err != nil {
		b.logger.Warn("event transport admission rejected",
			"type", string(e.Type), "error_class", "boundary")
		return
	}
	b.sendTransport(ctx, transport, e)
}

func (b *Bus) sendTransport(ctx context.Context, transport func(context.Context, repository.Event) error, e repository.Event) {
	if err := transport(ctx, e); err != nil {
		b.logger.Warn("event transport publish failed",
			"type", string(e.Type), "error_class", "transport")
	}
}

// DeliverContext broadcasts an event that originated on ANOTHER instance after
// rereading its exact persisted row and admitting the authoritative
// tenant/bucket. It never persists and never re-enters the transport, so a
// remote event is not fanned back out (no cross-replica notify storm). Forged
// or mismatched rows, bound buckets and faults are dropped; the authoritative
// row, not the caller's copy, is broadcast.
func (b *Bus) DeliverContext(ctx context.Context, e repository.Event) {
	b.mu.RLock()
	authority, boundary := b.authority, b.boundary
	b.mu.RUnlock()
	if authority == nil {
		b.logDeliverRejected("authority")
		return
	}
	authoritative, err := authority.Reread(ctx, e)
	if err != nil {
		b.logDeliverRejected("authority")
		return
	}
	if err := admitEventBucket(ctx, boundary, authoritative.TenantID, authoritative.Bucket); err != nil {
		b.logDeliverRejected("boundary")
		return
	}
	if err := ctx.Err(); err != nil {
		b.logDeliverRejected("context")
		return
	}
	b.broadcast(authoritative)
}

// Deliver is the legacy remote-delivery entry point. When an explicit
// authority was injected, or the event carries any identity, it delegates to
// DeliverContext so no unverified row is broadcast. Only the historical
// unscoped zero-ID wakeup shape keeps the local-only compatibility path; new
// wiring must use DeliverContext.
func (b *Bus) Deliver(e repository.Event) {
	b.mu.RLock()
	configured := b.authorityConfigured
	b.mu.RUnlock()
	if !configured && legacyUnscopedEvent(e) {
		b.broadcast(e)
		return
	}
	b.DeliverContext(context.Background(), e)
}

func unscopedEvent(e repository.Event) bool {
	return e.TenantID == "" && e.Bucket == "" && e.ObjectID == nil
}

func legacyUnscopedEvent(e repository.Event) bool {
	return e.ID <= 0 && unscopedEvent(e)
}

func (b *Bus) logDeliverRejected(errorClass string) {
	b.logger.Warn("event remote delivery rejected", "error_class", errorClass)
}

// admitEventBucket requires an explicit usable boundary; an unbound result is
// the only success. Missing capabilities and observation faults are never a
// legacy allow.
func admitEventBucket(ctx context.Context, boundary *ResourceBoundary, tenant, bucket string) error {
	if boundary == nil {
		return newResourceBoundaryFault(errors.New("missing resource boundary"))
	}
	return boundary.CheckBucket(ctx, tenant, bucket)
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
