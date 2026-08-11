package thumbnail

// Slot-before-open tests for GenerateContextWithOpener (direction:
// "Release the object stream before parking on the decode-slot semaphore").
// White-box: the unexported semaphore state (decodeSlots) is observable, and
// the no-arg acquireDecodeSlot/releaseDecodeSlot are used to saturate and
// drain all slots.
//
// Determinism discipline (mirrors context_test.go): every test that parks the
// caller holds all maxConcurrentDecodes slots before observing, so the send
// branch of the acquire select is never ready and the park is deterministic.
// Slot recovery is asserted after every API call via len(decodeSlots) — a
// leaked slot fails immediately.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// slotSaturate holds all maxConcurrentDecodes slots. Tests must pair it with
// `defer releaseAndRecoverSlots(t)` (context_test.go), which drains the
// saturating slots and re-verifies full capacity.
func slotSaturate() {
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
}

// assertSlotsReleased fails if any decode slot is still held after an API
// call (leak detection) and verifies full capacity by cycling 4
// acquire/release pairs — a leaked or lost slot would block the first
// acquire, an inflated capacity would exceed the buffered channel's size.
func assertSlotsReleased(t *testing.T) {
	t.Helper()
	if n := len(decodeSlots); n != 0 {
		t.Fatalf("decodeSlots not drained: %d slots held after API call", n)
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
}

// countingOpener builds an opener that records every invocation and serves
// data on a fresh NopCloser each time.
func countingOpener(data []byte, opens *atomic.Int64) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		opens.Add(1)
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

// TestSlotAcquiredBeforeOpen is the core ordering pin (AC-1): with all 4
// slots held, the caller parks before the opener runs — the open counter
// stays 0 while parked — and after one slot frees, the opener runs exactly
// once and the output decodes.
func TestSlotAcquiredBeforeOpen(t *testing.T) {
	slotSaturate()
	// The parked call takes one of the saturating slots (released below) and
	// returns it itself, so at cleanup exactly maxConcurrentDecodes-1
	// saturating slots remain held: drain those, then verify full capacity.
	defer func() {
		for i := 0; i < maxConcurrentDecodes-1; i++ {
			releaseDecodeSlot()
		}
		recoverSlots(t)
	}()

	var opens atomic.Int64
	var out []byte
	done := make(chan error, 1)
	go func() {
		var err error
		out, err = GenerateContextWithOpener(context.Background(), 16, 16, countingOpener(makePNG(t, 16, 16), &opens))
		done <- err
	}()

	// Parked window: all 4 slots are held, so the caller is deterministically
	// parked in acquireDecodeSlotContext's select — the opener is unreachable.
	time.Sleep(200 * time.Millisecond)
	if n := opens.Load(); n != 0 {
		t.Fatalf("opener invoked %d times while parked, want 0", n)
	}
	select {
	case err := <-done:
		t.Fatalf("call returned while parked: %v", err)
	default:
	}

	releaseDecodeSlot() // one slot frees; the parked caller must take it
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GenerateContextWithOpener: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GenerateContextWithOpener did not unblock after slot release")
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opener invoked %d times, want exactly once", n)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("output is not a decodable JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() > 16 || b.Dy() > 16 {
		t.Fatalf("output dims %dx%d exceed 16x16", b.Dx(), b.Dy())
	}
}

// TestSlotParkedCancelNeverOpens pins FR-3's cancel-while-parked lifecycle:
// the opener is never invoked and the error is ctx.Err().
func TestSlotParkedCancelNeverOpens(t *testing.T) {
	slotSaturate()
	defer releaseAndRecoverSlots(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var opens atomic.Int64
	done := make(chan error, 1)
	go func() {
		_, err := GenerateContextWithOpener(ctx, 16, 16, countingOpener(makePNG(t, 16, 16), &opens))
		done <- err
	}()

	time.Sleep(200 * time.Millisecond)
	if n := opens.Load(); n != 0 {
		t.Fatalf("opener invoked %d times while parked, want 0", n)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not unblock on cancel")
	}
	if n := opens.Load(); n != 0 {
		t.Fatalf("opener invoked %d times after cancel, want 0 (stream must never open)", n)
	}
}

// TestSlotDeadlineWhileParkedNeverOpens is the DeadlineExceeded companion of
// the cancel test (FR-3): a deadline firing while parked returns
// context.DeadlineExceeded without opening the stream and without consuming
// a slot.
func TestSlotDeadlineWhileParkedNeverOpens(t *testing.T) {
	slotSaturate()
	defer releaseAndRecoverSlots(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var opens atomic.Int64
	done := make(chan error, 1)
	go func() {
		_, err := GenerateContextWithOpener(ctx, 16, 16, countingOpener(makePNG(t, 16, 16), &opens))
		done <- err
	}()

	// Still parked (well inside the deadline, all slots held).
	time.Sleep(50 * time.Millisecond)
	if n := opens.Load(); n != 0 {
		t.Fatalf("opener invoked %d times while parked, want 0", n)
	}
	select {
	case err := <-done:
		t.Fatalf("call returned while parked: %v", err)
	default:
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not unblock on deadline")
	}
	if n := opens.Load(); n != 0 {
		t.Fatalf("opener invoked %d times after deadline, want 0 (stream must never open)", n)
	}
}

// TestSlotOpenErrorPassthrough pins FR-4's classification contract: an opener
// failure surfaces as *OpenError wrapping the exact error — never flattened
// to a decode sentinel or a bare context error — so the REST handler can
// classify it via writeError verbatim.
func TestSlotOpenErrorPassthrough(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := GenerateContextWithOpener(context.Background(), 16, 16, func() (io.ReadCloser, error) {
		return nil, fmt.Errorf("%w: open failed", sentinel)
	})
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want *OpenError", err)
	}
	if !errors.Is(oe.Err, sentinel) {
		t.Fatalf("oe.Err = %v, want wrapped sentinel", oe.Err)
	}
	if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrImageTooLarge) || errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("open error must not be a decode sentinel: %v", err)
	}
	assertSlotsReleased(t)

	// An opener that fails with a canceled request context must stay wrapped
	// in *OpenError (never flattened to a bare context error): pins the
	// OpenError-first ordering the REST classification relies on. The context
	// is canceled inside the opener — canceling before the call would abort
	// at acquisition, never reaching the opener.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = GenerateContextWithOpener(ctx, 16, 16, func() (io.ReadCloser, error) {
		cancel()
		return nil, ctx.Err()
	})
	if !errors.As(err, &oe) {
		t.Fatalf("canceled opener err = %v, want *OpenError", err)
	}
	if !errors.Is(oe.Err, context.Canceled) {
		t.Fatalf("oe.Err = %v, want context.Canceled", oe.Err)
	}
	assertSlotsReleased(t)
}

// TestSlotNilGuardErrors pins the programming-error guards: a nil opener and
// a (nil, nil) open result both return an error instead of panicking, and
// the slot is recovered in both cases.
func TestSlotNilGuardErrors(t *testing.T) {
	_, err := GenerateContextWithOpener(context.Background(), 16, 16, nil)
	if err == nil {
		t.Fatal("nil opener: want error, got nil")
	}
	assertSlotsReleased(t)

	_, err = GenerateContextWithOpener(context.Background(), 16, 16, func() (io.ReadCloser, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("nil stream with nil error: want error, got nil")
	}
	assertSlotsReleased(t)
}

// TestSlotDecodeSentinelsPreserved pins FR-4's disjointness: decode-pipeline
// sentinels (ErrImageTooLarge / ErrUnsupported / ErrMetadataTooLarge) pass
// through the new API unwrapped — never wrapped in *OpenError.
func TestSlotDecodeSentinelsPreserved(t *testing.T) {
	var opens atomic.Int64

	// Declared dims beyond MaxSourceDim → ErrImageTooLarge from the header,
	// payload never read.
	_, err := GenerateContextWithOpener(context.Background(), 16, 16, countingOpener(headerOnlyPNG(t, 8193, 8, 8, 6), &opens))
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversized dims: err = %v, want ErrImageTooLarge", err)
	}
	var oe *OpenError
	if errors.As(err, &oe) {
		t.Fatalf("oversized dims: decode sentinel wrapped as OpenError: %v", err)
	}
	assertSlotsReleased(t)

	// Non-image bytes → ErrUnsupported.
	_, err = GenerateContextWithOpener(context.Background(), 16, 16, countingOpener([]byte("definitely not an image"), &opens))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("garbage bytes: err = %v, want ErrUnsupported", err)
	}
	assertSlotsReleased(t)

	// Metadata flood → ErrMetadataTooLarge.
	_, err = GenerateContextWithOpener(context.Background(), 16, 16, countingOpener(appnPaddedJPEG(t, MaxMetadataBytes+1<<20), &opens))
	if !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("metadata flood: err = %v, want ErrMetadataTooLarge", err)
	}
	assertSlotsReleased(t)
}

// TestSlotOutputParityWithGenerateContext pins NFR-3: the new API runs the
// exact decode pipeline, so output is byte-identical to GenerateContext for
// the same input — including the clamp paths (zero and oversized dims).
func TestSlotOutputParityWithGenerateContext(t *testing.T) {
	data := makePNG(t, 64, 64)
	for _, dims := range [][2]int{{32, 32}, {0, 0}, {99999, 99999}} {
		want, err := GenerateContext(context.Background(), bytes.NewReader(data), dims[0], dims[1])
		if err != nil {
			t.Fatalf("GenerateContext(%d,%d): %v", dims[0], dims[1], err)
		}
		var opens atomic.Int64
		got, err := GenerateContextWithOpener(context.Background(), dims[0], dims[1], countingOpener(data, &opens))
		if err != nil {
			t.Fatalf("GenerateContextWithOpener(%d,%d): %v", dims[0], dims[1], err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("output differs from GenerateContext for dims %dx%d", dims[0], dims[1])
		}
		assertSlotsReleased(t)
	}
}
