package thumbnail

// Behavioral semaphore pins. The aggregate allocation and held-frame memory
// bounds live in semaphore_memory_test.go; no polling of a moving GC heap.

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// TestAcquireDecodeSlotContextCanceledReleaseRace is the regression pin for
// the claimed post-send-cancellation deadlock (direction: "Fix deadlock in
// acquireDecodeSlotContext's post-send cancellation path"). The claim: a
// concurrent releaseDecodeSlot landing between the winning send and the
// post-check receive empties the channel and parks the receive forever,
// collapsing the maxConcurrentDecodes=4 ceiling. The invariant argument
// (requirements spec §1.1) shows the park is unreachable from a clean 1:1
// acquire/release pairing; this test pins that the post-check release is
// non-blocking by construction and that the channel state stays consistent
// under the exact claimed interleaving, so a future pairing violation cannot
// silently reintroduce the park.
//
// Determinism: the test holds maxConcurrentDecodes-1 slots (one free) and
// cancels the context BEFORE spawning the acquire goroutine, so both select
// branches are ready when the goroutine runs — the send branch wins with
// P ≈ 1/2 per iteration (P(never over 500) = 2⁻⁵⁰⁰), driving the
// send→post-check path hundreds of times. The main goroutine's per-iteration
// releaseDecodeSlot() is the "concurrent release landing in the window"; the
// released slot is re-acquired each iteration so the held-slot baseline is
// invariant. Every iteration must return context.Canceled within a per-
// iteration watchdog (2 s) — a parked post-check would blow past it.
// No sleeps are used as timing primitives; the watchdogs are failure
// detectors only.
func TestAcquireDecodeSlotContextCanceledReleaseRace(t *testing.T) {
	// Phase 1: storm loop with maxConcurrentDecodes-1 slots held (both-ready window).
	for i := 0; i < maxConcurrentDecodes-1; i++ {
		acquireDecodeSlot()
	}
	const iters = 500
	for i := 0; i < iters; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // canceled BEFORE spawn: both branches ready at the select

		done := make(chan error, 1)
		go func() {
			done <- acquireDecodeSlotContext(ctx)
		}()

		releaseDecodeSlot() // "concurrent release landing in the window" (never blocks: buffer ≥ 3)

		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("iteration %d: err = %v, want context.Canceled", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: acquire parked — post-check release blocked past watchdog", i)
		}
		acquireDecodeSlot() // restore the released slot (ready: buffer ≤ 3)
	}

	// Phase 2: channel-state consistency — exactly maxConcurrentDecodes-1 held,
	// nothing consumed, nothing leaked (full capacity re-verified by recoverSlots).
	for i := 0; i < maxConcurrentDecodes-1; i++ {
		releaseDecodeSlot()
	}
	recoverSlots(t)

	// Phase 3: ceiling probe (TestGenerateSemaphoreBoundsConcurrentAllocation-
	// style, deterministic and cheap — runs under -race, unlike the ~1.2 GiB
	// test): at most maxConcurrentDecodes concurrent holders.
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot() // hold all 4
	}
	blocked := make(chan struct{})
	go func() {
		acquireDecodeSlot() // 5th: must block
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("5th acquire completed with all slots held — ceiling breached")
	case <-time.After(200 * time.Millisecond):
		// blocked: correct
	}
	releaseDecodeSlot() // unblock the 5th
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked acquire did not complete after release — slot leak?")
	}
	releaseAndRecoverSlots(t) // drains the 4 held (3 test + 1 goroutine), re-verifies full capacity
}

// signalReader blocks on Read until closed; used to prove a Generate call is
// parked before it touches the input stream, then unblocked so the parked
// goroutine exits cleanly (no slot/goroutine leak into later tests).
type signalReader struct {
	c chan struct{}
}

func (r *signalReader) Read([]byte) (int, error) {
	<-r.c
	return 0, errors.New("reader closed")
}

func TestSemaphoreBlocksBeforeDecodeConfig(t *testing.T) {
	// C2 deterministic test 1: with every slot held, a Generate call must
	// park at acquisition — before DecodeConfig, before any input read.
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	// No defer release here: the slots are released mid-test to unblock the
	// parked goroutine (below), and the final acquire/release cycle verifies
	// full recovery. A t.Fatal in the parked-branch leaks slots, which is
	// fine — the test has already failed and the process exits.

	r := &signalReader{c: make(chan struct{})}
	entered := make(chan struct{})
	go func() {
		// A read from the stream is the first observable effect of the
		// decode section; if acquisition is correctly placed before the
		// LimitReader/DecodeConfig, this goroutine never reads while
		// parked.
		_, _ = Generate(r, 8, 8)
		close(entered)
	}()
	select {
	case <-entered:
		t.Fatal("Generate returned while all slots held — acquisition is not before decode")
	case <-time.After(200 * time.Millisecond):
		// parked: correct
	}
	// Unblock the parked goroutine: first release the slots so it can enter
	// the decode section, then close the reader so its first Read fails and
	// Generate returns (releasing its slot via defer). Wait for a clean
	// exit so no slot or goroutine leaks into later tests.
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
	close(r.c)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("parked Generate did not exit after reader close — slot leak?")
	}
	// The goroutine's deferred release must have returned its slot: all
	// slots are acquirable again.
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
}

func TestSemaphoreReleasesOnError(t *testing.T) {
	// C2 deterministic test 2: an error path (undecodable input) must return
	// the slot — otherwise the package degrades to permanent DoS after one
	// bad request. After Generate fails, all slots must be acquirable.
	_, err := Generate(bytes.NewReader([]byte("not an image")), 8, 8)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
}

// panicReader panics on first read, modelling a reader fault mid-decode.
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("boom: reader fault") }

func TestSemaphoreReleasesOnPanic(t *testing.T) {
	// C2 deterministic test 3: the defer-based release must survive a panic
	// inside the decode section; a leaked slot would wedge the package.
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from reader")
		}
		// Panic recovered: Generate's deferred release has run; every slot
		// must now be acquirable (no leak).
		for i := 0; i < maxConcurrentDecodes; i++ {
			acquireDecodeSlot()
		}
		for i := 0; i < maxConcurrentDecodes; i++ {
			releaseDecodeSlot()
		}
	}()
	_, _ = Generate(panicReader{}, 8, 8)
}

// lateCancelCtx is the fault-injection context for the pairing-violation
// seed: Done() never fires (so the acquire's select deterministically picks
// the send branch — no 50/50 select race) while Err() reports Canceled —
// exactly the "canceled between the select and the post-send re-check"
// window, made deterministic.
type lateCancelCtx struct {
	context.Context
	done chan struct{}
	err  error
}

func (c *lateCancelCtx) Done() <-chan struct{} { return c.done }
func (c *lateCancelCtx) Err() error            { return c.err }

// TestAcquireDecodeSlotContextPairingViolationFailsClosed is the seeded
// pairing-violation fault-injection (QA P1): a mispaired release (an extra
// receive) steals the token an acquire just placed, so the acquire's
// slot-return sees an empty buffer — the exact state where a blocking
// `<-decodeSlots` would park forever. Deterministic via channel FIFO: the
// thief's receive is queued before the acquire's send, so Go's channel
// delivery gives it the token; the acquire's own return then finds the
// buffer empty and must fail toward over-restriction (the default branch)
// instead of parking. Channel repaired and full capacity re-verified.
func TestAcquireDecodeSlotContextPairingViolationFailsClosed(t *testing.T) {
	// Empty the buffer so the thief's receive queues (FIFO) rather than
	// consuming a legitimate token.
	slotSaturate()
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
	if n := len(decodeSlots); n != 0 {
		t.Fatalf("seed: decodeSlots len %d, want 0", n)
	}

	// Canceled-between-select-and-recheck, made deterministic (see
	// lateCancelCtx): the send branch always wins; the re-check always aborts.
	ctx := &lateCancelCtx{
		Context: context.Background(),
		done:    make(chan struct{}),
		err:     context.Canceled,
	}

	thieved := make(chan struct{})
	go func() {
		// Mispaired release: queued on the empty buffer BEFORE the acquire's
		// send, so FIFO delivery gives it the token the send places.
		<-decodeSlots
		close(thieved)
	}()
	// Settle: let the thief reach the channel and queue. With the acquire
	// launched after, the steal ordering holds ~always; in the pathological
	// case the acquire self-returns its own token and every assertion below
	// still holds (the thief then stays queued for the next send — a
	// persistent mispaired release, exactly the seeded fault).
	time.Sleep(time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- acquireDecodeSlotContext(ctx) }()

	// The acquire's slot-return runs against an empty buffer (the thief was
	// queued first and takes the token at the send): the non-blocking
	// default branch must return ctx.Err() (the old blocking receive parked
	// forever here — watchdog-detected deadlock).
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire parked after canceled ctx with an empty buffer — pairing-violation deadlock; non-blocking slot-return missing")
	}
	// The thief must have received a token — either the acquire's (the
	// seeded steal; the default branch fired and no phantom token exists)
	// or, in the pathological scheduling ordering, the acquire self-returned
	// its own token and the thief is still queued: feed it one so the seed
	// completes deterministically (the fed token is itself a mispaired
	// acquire — the violation persists until repaired below).
	select {
	case <-thieved:
	case <-time.After(time.Second):
		acquireDecodeSlot()
		select {
		case <-thieved:
		case <-time.After(5 * time.Second):
			t.Fatal("mispaired release never completed — the channel is wedged")
		}
	}
	if n := len(decodeSlots); n != 0 {
		t.Fatalf("decodeSlots len %d after violation, want 0 (no phantom token; over-restriction)", n)
	}
	// Repair + full-capacity re-verification: the channel must still admit
	// maxConcurrentDecodes acquires (no leaked/corrupted state).
	recoverSlots(t)
}
