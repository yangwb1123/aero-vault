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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
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

// ctxBlockingReader serves data, then blocks on ctx.Done() and returns
// ctx.Err(). blocked (optional) is closed once, exactly when the first
// post-data Read is attempted.
//
// The data prefix must be exactly what image/png's DecodeConfig consumes (PNG
// signature + IHDR = 33 bytes, no read-ahead), so the block deterministically
// occurs in the decode section — mid-decode-section — never during the config
// scan, and the slot is genuinely held (unlike the acquisition tests, which
// park before the decode starts). With the PNG eXIf walk in place the first
// post-IHDR read for PNG is the pre-Decode orientation walk (still
// mid-decode-section, slot held, same error contract), so the block lands
// there.
type ctxBlockingReader struct {
	ctx     context.Context
	data    []byte
	off     int
	blocked chan struct{} // may be nil
	signal  sync.Once
}

func (r *ctxBlockingReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	if r.blocked != nil {
		r.signal.Do(func() { close(r.blocked) })
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

// TestGenerateContextPreservesDeadlineMidDecode pins the mid-decode deadline
// path (FR-1/AC-1): a deadline that fires while Decode reads the stream —
// after slot acquisition — must surface as context.DeadlineExceeded, never be
// flattened to ErrUnsupported. Unlike the acquisition tests, no slots are
// held: the decode must genuinely start. The deadline self-fires (bounded
// ~50 ms, mirroring TestGenerateContextDeadlineExceededWhileParked).
func TestGenerateContextPreservesDeadlineMidDecode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	reader := &ctxBlockingReader{ctx: ctx, data: makePNG(t, 64, 64)[:33]}

	_, err := GenerateContext(ctx, reader, 32, 32)
	if err == nil {
		t.Fatal("GenerateContext succeeded, want context.DeadlineExceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
	}
}

// TestGenerateContextPreservesCancelMidDecode is the cancellation companion
// (backs the REST silent-return contract): a cancel that fires mid-decode must
// surface as context.Canceled, never ErrUnsupported. Fully handshake-driven
// (no sleeps): the reader signals when it is blocked inside Decode, then the
// test cancels and joins.
func TestGenerateContextPreservesCancelMidDecode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := make(chan struct{})
	reader := &ctxBlockingReader{ctx: ctx, data: makePNG(t, 64, 64)[:33], blocked: blocked}

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, 32, 32)
		done <- err
	}()
	<-blocked // reader is parked in the pre-Decode orientation walk
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not unblock on mid-decode cancel")
	}
}

// gifConfigPrefix is the first 13 bytes of a GIF89a stream: the 6-byte
// signature and the 7-byte Logical Screen Descriptor, with the global color
// table flag set (packed 0x80, gctSize 0 → 2 entries). image/gif's
// DecodeConfig consumes exactly these bytes before it must read the global
// color table (Go 1.26 reader.go: readHeaderAndScreenDescriptor reads the 13
// header+LSD bytes, then readColorTable reads 3·2^(gctSize+1) = 6 more), so
// serving this prefix and blocking makes the block land deterministically
// inside the config scan — at the global-color-table read, never in Decode.
func gifConfigPrefix() []byte {
	return []byte{
		'G', 'I', 'F', '8', '9', 'a', // signature
		0x40, 0x00, // width 64 (LE)
		0x40, 0x00, // height 64 (LE)
		0x80, // packed: global color table flag, gctSize 0 → 2 entries
		0x00, // background color index
		0x00, // pixel aspect ratio
	}
}

// TestGenerateContextPreservesDeadlineMidDecodeConfig pins the
// DecodeConfig-site ctx branches (QA-2): a deadline that fires while the
// config scan is blocked reading the GIF global color table must surface as
// context.DeadlineExceeded, never ErrUnsupported — the same identity
// contract the mid-Decode tests pin, on the config-scan path (the path the
// MaxMetadataBytes budget exists to bound). Unlike the acquisition tests, no
// slots are held: the decode genuinely starts. The deadline self-fires
// (~50 ms, mirroring TestGenerateContextPreservesDeadlineMidDecode).
func TestGenerateContextPreservesDeadlineMidDecodeConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	reader := &ctxBlockingReader{ctx: ctx, data: gifConfigPrefix()}

	_, err := GenerateContext(ctx, reader, 32, 32)
	if err == nil {
		t.Fatal("GenerateContext succeeded, want context.DeadlineExceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
	}
}

// errAfterDataReader serves data, then returns a fixed error on every
// further read — the QA-3 fallback shape: the stream fails with a wrapped
// context sentinel while the context itself stays healthy, so only the
// errors.Is fallback branches in GenerateContext can classify it.
// image/png propagates the reader error unwrapped at both decode sites
// (Go 1.26 reader.go: checkHeader/parseChunk return the io.ReadFull error
// as-is; only io.EOF is substituted), so identity survives to the module
// boundary.
//
// The served prefix must be shorter than what the decoder needs: 8 bytes
// (PNG signature only) fails DecodeConfig at the chunk-header read, 33
// bytes (signature + IHDR, exactly what the config scan consumes) lets
// DecodeConfig succeed and fails the pre-Decode orientation walk's first
// post-IHDR chunk read (Decode would re-encounter the same bytes/error).
type errAfterDataReader struct {
	data []byte
	off  int
	err  error
}

func (r *errAfterDataReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.err
}

// TestGenerateContextPreservesWrappedErrorMidDecodeConfig pins the errors.Is
// fallback at the DecodeConfig site (QA-3): a stream that fails with a
// wrapped context sentinel while the context is healthy must surface the
// same wrapped instance — never ErrUnsupported and never a re-wrapped or
// bare copy. A wrong implementation that returned a bare sentinel (ctx.Err()
// path) or flattened to ErrUnsupported fails the instance equality
// assertion.
func TestGenerateContextPreservesWrappedErrorMidDecodeConfig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sentinel error
	}{
		{"DeadlineExceeded", context.DeadlineExceeded},
		{"Canceled", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := fmt.Errorf("wrapped: %w", tc.sentinel)
			reader := &errAfterDataReader{data: makePNG(t, 64, 64)[:8], err: want}
			_, err := GenerateContext(context.Background(), reader, 32, 32)
			if err != want {
				t.Fatalf("err = %v, want the same wrapped instance %v", err, want)
			}
			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
			}
		})
	}
}

// TestGenerateContextPreservesWrappedErrorMidDecode pins the same fallback
// in the decode section (QA-3): the full 33-byte config prefix is served
// (DecodeConfig succeeds), then the stream fails with the wrapped sentinel
// on the first post-header read — which, for PNG, is the pre-Decode
// orientation walk's first chunk-header read (the walk returns the raw
// wrapped instance and generateLocked's classification surfaces it
// unchanged). Same instance assertion as the DecodeConfig-site variant.
func TestGenerateContextPreservesWrappedErrorMidDecode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sentinel error
	}{
		{"DeadlineExceeded", context.DeadlineExceeded},
		{"Canceled", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := fmt.Errorf("wrapped: %w", tc.sentinel)
			reader := &errAfterDataReader{data: makePNG(t, 64, 64)[:33], err: want}
			_, err := GenerateContext(context.Background(), reader, 32, 32)
			if err != want {
				t.Fatalf("err = %v, want the same wrapped instance %v", err, want)
			}
			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
			}
		})
	}
}

// gatedReader serves r's bytes but holds the first Read on proceed (closing
// blocked once, on the first Read attempt). It makes a deadline/cancel
// deterministically precede the decode outcome it must be ordered against:
// no stream bytes flow until the test opens the gate, so the ordering
// assertion never depends on wall-clock races between the reader and the
// test.
type gatedReader struct {
	r       io.Reader
	blocked chan struct{}
	proceed chan struct{}
	signal  sync.Once
	gate    sync.Once
}

func (g *gatedReader) Read(p []byte) (int, error) {
	g.signal.Do(func() { close(g.blocked) })
	g.gate.Do(func() { <-g.proceed })
	return g.r.Read(p)
}

// TestGenerateContextMetadataBudgetWinsOverDeadline pins the D3 ordering at
// the DecodeConfig site (QA-4): when the metadata budget overflows at the
// same time the request deadline has already fired, ErrMetadataTooLarge must
// win — the budget abort is an internal contract distinct from request
// lifecycle, and its priority predates this fix. The gate makes the overlap
// deterministic: the reader refuses to serve the 9 MiB APP1-padded fixture
// until the deadline has fired, so ctx.Err() is guaranteed non-nil when the
// tee overflows at MaxMetadataBytes. A reordering that checked ctx before
// the budget would return the deadline error and fail this test.
func TestGenerateContextMetadataBudgetWinsOverDeadline(t *testing.T) {
	payload := appnPaddedJPEG(t, MaxMetadataBytes+1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	blocked := make(chan struct{})
	proceed := make(chan struct{})
	reader := &gatedReader{r: bytes.NewReader(payload), blocked: blocked, proceed: proceed}
	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, 100, 100)
		done <- err
	}()
	select {
	case <-blocked: // first read attempted: slot acquired, config scan started
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext never started reading (acquire did not return)")
	}
	<-ctx.Done() // let the deadline fire while the reader is gated
	close(proceed)
	select {
	case err := <-done:
		if !errors.Is(err, ErrMetadataTooLarge) {
			t.Fatalf("err = %v, want ErrMetadataTooLarge (budget wins over the expired deadline)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not return after budget overflow")
	}
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
