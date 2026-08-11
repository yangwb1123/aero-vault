package thumbnail

// Context-aware decode-slot acquisition tests (direction: "Make decode-slot
// acquisition context-aware so blocked waiters release held object streams").
// White-box: the unexported semaphore state (decodeSlots) is observable, and
// the no-arg acquireDecodeSlot is used to hold all slots.
//
// Determinism discipline: every test holds all maxConcurrentDecodes slots
// before canceling, so the send branch of the acquire select is never ready
// and the ctx.Done branch is the only ready one — no both-ready randomness.
// The post-select ctx re-check (acquireDecodeSlotContext) additionally makes
// the canceled-ctx outcome deterministic in every channel state.

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// readTracker records whether Read was ever invoked, returning EOF on the
// first read. It deliberately does not block like signalReader: a contract
// regression (stream consumed while parked, or a decode run for a canceled
// caller) must fail the "stream untouched" assertion instead of hanging.
type readTracker struct {
	read atomic.Bool
}

func (t *readTracker) Read([]byte) (int, error) {
	t.read.Store(true)
	return 0, io.EOF
}

// releaseAndRecoverSlots releases the slots held by the calling test (which
// must hold exactly maxConcurrentDecodes), then asserts full semaphore
// capacity: every slot must be acquirable and releasable again (mirrors the
// final block of TestSemaphoreBlocksBeforeDecodeConfig). A consumed or leaked
// slot fails loudly here.
func releaseAndRecoverSlots(t *testing.T) {
	t.Helper()
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
	recoverSlots(t)
}

// recoverSlots asserts full semaphore capacity without releasing anything:
// every slot must be acquirable and releasable again. Call after releasing
// the slots the test itself held.
func recoverSlots(t *testing.T) {
	t.Helper()
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
}

func TestGenerateContextCanceledBeforeCall(t *testing.T) {
	// AC-1: hold all slots, call GenerateContext with an already-canceled
	// context; it returns promptly without reading the stream and without
	// consuming or leaking a slot.
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	defer releaseAndRecoverSlots(t)

	tracker := &readTracker{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before the call

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, tracker, 8, 8)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not return promptly on canceled context")
	}
	if tracker.read.Load() {
		t.Fatal("input stream was read while context canceled — parked wait consumed the pinned stream")
	}
}

func TestGenerateContextCanceledWithFreeSlot(t *testing.T) {
	// Both-branches-ready window (design §2.4): with at least one free slot
	// and a canceled context, the acquire select picks randomly between the
	// send and ctx.Done. Either outcome must satisfy the same contract —
	// unwrapped ctx.Err(), stream untouched, no slot consumed or leaked — so
	// the assertions are deterministic; the post-select re-check in
	// acquireDecodeSlotContext is what makes the send branch safe (the slot
	// is returned instead of decoding for a dead caller). Repeated iterations
	// cover both branches with near-certainty (P(all ctx.Done) = 2^-100).
	for i := 0; i < maxConcurrentDecodes-1; i++ {
		acquireDecodeSlot() // one slot stays free
	}
	defer func() {
		// Release only the maxConcurrentDecodes-1 slots this test held, then
		// verify full capacity: neither select outcome may consume or leak a
		// slot.
		for i := 0; i < maxConcurrentDecodes-1; i++ {
			releaseDecodeSlot()
		}
		recoverSlots(t)
	}()

	tracker := &readTracker{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 100; i++ {
		_, err := GenerateContext(ctx, tracker, 8, 8)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: err = %v, want context.Canceled", i, err)
		}
	}
	if tracker.read.Load() {
		t.Fatal("input stream was read while context canceled")
	}
}

func TestGenerateContextUnblocksParkedWaiterOnCancel(t *testing.T) {
	// The actual DoS shape: a waiter parked at acquisition (all slots held)
	// must unblock when its context is canceled — releasing the pinned object
	// stream — instead of parking until queue drain.
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	defer releaseAndRecoverSlots(t)

	tracker := &readTracker{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, tracker, 8, 8)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("GenerateContext returned while all slots held (err = %v) — not parked at acquisition", err)
	case <-time.After(200 * time.Millisecond):
		// parked: correct — the goroutine cannot acquire while slots are held.
	}
	// Slots must remain held until after cancel: if a slot were free, both
	// select branches would be ready and the outcome would be nondeterministic.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parked GenerateContext did not unblock on cancel — waiter still pins the stream")
	}
	if tracker.read.Load() {
		t.Fatal("input stream was read while context canceled — parked wait consumed the pinned stream")
	}
}

func TestGenerateContextDeadlineExceededWhileParked(t *testing.T) {
	// Deadline variant of the parked-waiter unblock: a request-level deadline
	// (REQUEST_TIMEOUT_SECONDS on the thumbnail route) must release a parked
	// waiter with context.DeadlineExceeded, not just client cancellation.
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	defer releaseAndRecoverSlots(t)

	tracker := &readTracker{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, tracker, 8, 8)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parked GenerateContext did not unblock on deadline — waiter still pins the stream")
	}
	if tracker.read.Load() {
		t.Fatal("input stream was read after deadline — parked wait consumed the pinned stream")
	}
}
