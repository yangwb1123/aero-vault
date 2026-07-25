package events

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestBus_TransportHookLocalOnly verifies the loop-free wiring contract:
//   - Publish (local origin) persists, broadcasts locally, AND calls the transport.
//   - Deliver (remote origin) broadcasts locally only — no persist, no transport —
//     so a remote event is never fanned back out (no cross-replica notify storm).
func TestBus_TransportHookLocalOnly(t *testing.T) {
	repo := &fakeRepo{}
	bus := New(repo, quietLogger())

	var transportCalls int32
	bus.WithTransport(func(_ context.Context, _ repository.Event) error {
		atomic.AddInt32(&transportCalls, 1)
		return nil
	})
	sub, _ := bus.Subscribe()

	// Local publish.
	bus.Publish(context.Background(), repository.Event{Type: repository.EventCreated, Key: "local"})
	if ev, ok := recv(t, sub, time.Second); !ok || ev.Key != "local" {
		t.Fatal("expected local event delivered to subscriber")
	}
	if repo.insertCount() != 1 {
		t.Fatalf("local publish should persist once, got %d", repo.insertCount())
	}
	if got := atomic.LoadInt32(&transportCalls); got != 1 {
		t.Fatalf("local publish should call transport once, got %d", got)
	}

	// Remote delivery.
	bus.Deliver(repository.Event{Type: repository.EventCreated, Key: "remote"})
	if ev, ok := recv(t, sub, time.Second); !ok || ev.Key != "remote" {
		t.Fatal("expected delivered remote event on subscriber")
	}
	if repo.insertCount() != 1 {
		t.Fatalf("Deliver must NOT persist (origin already did); inserts=%d", repo.insertCount())
	}
	if got := atomic.LoadInt32(&transportCalls); got != 1 {
		t.Fatalf("Deliver must NOT re-enter transport (loop guard); calls=%d", got)
	}
}

// TestBus_DropsOnBackpressure verifies the drop counter increments when a
// subscriber's 64-deep buffer overflows and is never drained.
func TestBus_DropsOnBackpressure(t *testing.T) {
	repo := &fakeRepo{}
	bus := New(repo, quietLogger())
	_, _ = bus.Subscribe() // intentionally never drained

	for i := 0; i < 200; i++ {
		bus.Publish(context.Background(), repository.Event{Type: repository.EventCreated, Key: "k"})
	}
	if bus.Dropped() == 0 {
		t.Fatal("expected dropped events once the 64-buffer overflows without draining")
	}
}
