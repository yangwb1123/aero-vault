package thumbnail

// In-encode cancellation tests: the jpeg.Encode phase is the last cancel
// blind spot in the pipeline (previously every phase boundary was checked but
// the encode ran to completion with no ctx consultation). The fix is a
// context-checking writer (encode_writer.go) used directly by jpeg.Encode;
// these tests pin the writer contract deterministically (unit arm) and the
// full-pipeline wiring + slot release (integration arm).

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"
	"time"
)

// TestGenerateContextAbortsMidEncode is the integration pin for the in-encode
// boundary: a cancel that lands after the pre-encode check (C3), with the
// stream fully drained and the pipeline already past config/decode/scale/
// composite, must abort inside jpeg.Encode — via ctxWriter's first write
// after the cancel — and release the decode slot. Fixture: an opaque 2048²
// PNG at target HardMax×HardMax, so scale is a ratio ≥ 1 no-op,
// compositeOnWhite is an opaque no-op, and there is no EXIF: the encode is
// ~90%+ of the drain→done interval, making it the discriminator for the
// in-encode abort.
//
// Calibration is machine-adaptive: two uncanceled warm-up runs measure the
// drain→done interval D (min-of-2 — the fastest warm-up best approximates the
// cancel run's best case, strictly narrowing the "cancel lands after done"
// false-failure direction), and the cancel run lands the cancel at D −
// max(5 ms, D/4) after the drain handshake — inside the encode tail on any
// runner speed, with a ≥ max(5 ms, D/4) margin against the pipeline beating
// the warm-up. Each warm-up run has a 5 s watchdog: a 2048² pipeline is
// ~100 ms here, so 5 s is a fixture/regression bound, not a timing
// assumption. Pre-fix, a cancel landing mid-encode runs the encode to
// completion and returns (JPEG, nil) → red; post-fix the writer aborts at the
// first write (~150 µs) → Canceled → green. False-failure directions are
// bounded: a cancel landing before the encode aborts at C2/C3 (Canceled,
// green); one landing after the pipeline completed requires the actual run to
// beat the warm-up by ≥ max(5 ms, D/4) (the calibration margin); one landing
// after the final flush is caught by the terminal check (Canceled, green).
// The writer contract itself is pinned deterministically by
// TestEncodeWriterHonorsContext, so A1 is immune to CI timing on the abort
// path it is responsible for.
func TestGenerateContextAbortsMidEncode(t *testing.T) {
	data := makePNG(t, 2048, 2048)

	// Warm-up (min-of-2, F3): measure drain→done so the cancel lands inside
	// the encode tail. The interval starts at the drain handshake (final byte
	// served) and ends when GenerateContext returns — on this pipeline that
	// is the decode finalize + the full encode (encode ≈ 90%+ of the
	// interval). Two runs, taking the minimum, bound the false-failure
	// direction: the cancel run would have to beat the FASTEST warm-up by
	// >25% of D to land the cancel after the pipeline completed.
	var d time.Duration
	for i := 0; i < 2; i++ {
		consumed := make(chan struct{})
		reader := &drainReader{data: data, consumed: consumed}
		done := make(chan error, 1)
		go func() {
			_, err := GenerateContext(context.Background(), reader, HardMax, HardMax)
			done <- err
		}()
		select {
		case <-consumed:
		case <-time.After(5 * time.Second):
			t.Fatal("warm-up GenerateContext never drained the stream (watchdog)")
		}
		start := time.Now()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("warm-up GenerateContext failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("warm-up GenerateContext did not complete (watchdog)")
		}
		if elapsed := time.Since(start); i == 0 || elapsed < d {
			d = elapsed
		}
	}
	sleep := d - max(5*time.Millisecond, d/4)

	consumed := make(chan struct{})
	reader := &drainReader{data: data, consumed: consumed}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, HardMax, HardMax)
		done <- err
	}()
	select {
	case <-consumed:
	case <-time.After(15 * time.Second):
		t.Fatal("GenerateContext never drained the stream")
	}
	time.Sleep(sleep)
	cancel()
	var cerr error
	select {
	case cerr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not abort after mid-encode cancel")
	}
	if !errors.Is(cerr, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (in-encode abort)", cerr)
	}
	if errors.Is(cerr, ErrUnsupported) {
		t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", cerr)
	}
	// The join above (5 s bound) ran before this assertion, so the dead call's
	// defer-registered release has already executed: slot recovery cannot
	// cascade a leaked slot into later tests (QA finding, dispositioned).
	assertSlotsReleased(t)
}

// TestEncodeWriterHonorsContext deterministically pins the ctxWriter contract
// (REQ-1/REQ-2/REQ-3): no timing, no machine dependence. Live ctx → pure
// pass-through with byte-identical jpeg.Encode output; canceled ctx → exact
// context.Canceled with nothing written; expired-deadline ctx → exact
// context.DeadlineExceeded with nothing written (the 504 leg).
func TestEncodeWriterHonorsContext(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 128, 255})
		}
	}
	opts := &jpeg.Options{Quality: quality}

	t.Run("live", func(t *testing.T) {
		// Pass-through: writes delegate verbatim while the ctx is alive.
		var buf bytes.Buffer
		w := &ctxWriter{ctx: context.Background(), buf: &buf}
		p := []byte("hello, jpeg")
		n, err := w.Write(p)
		if err != nil || n != len(p) {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(p))
		}
		if !bytes.Equal(buf.Bytes(), p) {
			t.Fatalf("Write did not delegate verbatim: %q", buf.Bytes())
		}
		if err := w.WriteByte(0xAB); err != nil {
			t.Fatalf("WriteByte = %v, want nil", err)
		}
		if got := buf.Bytes()[len(p)]; got != 0xAB {
			t.Fatalf("WriteByte wrote %#x, want 0xab", got)
		}
		if err := w.Flush(); err != nil {
			t.Fatalf("Flush = %v, want nil", err)
		}
		// Write(nil) on a live ctx passes through untouched: (0, nil), no
		// buffered output (F5).
		if n, err := w.Write(nil); n != 0 || err != nil {
			t.Fatalf("Write(nil) = (%d, %v), want (0, nil)", n, err)
		}
		// jpeg.Encode through the writer is byte-identical to a raw encode —
		// the zero-output-risk property (REQ-3), pinned here and by the
		// full-pipeline parity anchors (TestSlotOutputParityWithGenerateContext).
		var a, b bytes.Buffer
		if err := jpeg.Encode(&ctxWriter{ctx: context.Background(), buf: &a}, img, opts); err != nil {
			t.Fatalf("jpeg.Encode via ctxWriter: %v", err)
		}
		if err := jpeg.Encode(&b, img, opts); err != nil {
			t.Fatalf("jpeg.Encode reference: %v", err)
		}
		if !bytes.Equal(a.Bytes(), b.Bytes()) {
			t.Fatalf("encode via ctxWriter differs from raw encode (%d vs %d bytes)", a.Len(), b.Len())
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var buf bytes.Buffer
		w := &ctxWriter{ctx: ctx, buf: &buf}
		if n, err := w.Write([]byte("x")); n != 0 || err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Write = (%d, %v), want (0, context.Canceled)", n, err)
		}
		if buf.Len() != 0 {
			t.Fatalf("Write wrote %d bytes on a canceled ctx", buf.Len())
		}
		if err := w.WriteByte(0); !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteByte = %v, want context.Canceled", err)
		}
		if err := w.Flush(); !errors.Is(err, context.Canceled) {
			t.Fatalf("Flush = %v, want context.Canceled", err)
		}
		// Write(nil) and write-after-error: every call on a done ctx re-checks
		// ctx.Err() — no len(p) shortcut, no cached/once state (F5). A
		// regression to (len(p), nil) on a done ctx would silently truncate
		// the JPEG with nil error.
		if n, err := w.Write(nil); n != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("Write(nil) = (%d, %v), want (0, context.Canceled)", n, err)
		}
		if n, err := w.Write([]byte("again")); n != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("Write after failed Flush = (%d, %v), want (0, context.Canceled)", n, err)
		}
		var out bytes.Buffer
		if err := jpeg.Encode(&ctxWriter{ctx: ctx, buf: &out}, img, opts); !errors.Is(err, context.Canceled) {
			t.Fatalf("jpeg.Encode err = %v, want context.Canceled", err)
		}
		if out.Len() != 0 {
			t.Fatalf("jpeg.Encode wrote %d bytes on a canceled ctx", out.Len())
		}
	})

	t.Run("deadline", func(t *testing.T) {
		// Already-expired deadline: ctx.Err() returns DeadlineExceeded
		// immediately — the 504 leg (REST maps it to service.ErrTimeout).
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		var buf bytes.Buffer
		w := &ctxWriter{ctx: ctx, buf: &buf}
		if n, err := w.Write([]byte("x")); n != 0 || err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Write = (%d, %v), want (0, context.DeadlineExceeded)", n, err)
		}
		if err := w.WriteByte(0); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WriteByte = %v, want context.DeadlineExceeded", err)
		}
		if err := w.Flush(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Flush = %v, want context.DeadlineExceeded", err)
		}
		// Write(nil) on an expired-deadline ctx returns the deadline error —
		// the same per-call re-check (F5).
		if n, err := w.Write(nil); n != 0 || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Write(nil) = (%d, %v), want (0, context.DeadlineExceeded)", n, err)
		}
		var out bytes.Buffer
		if err := jpeg.Encode(&ctxWriter{ctx: ctx, buf: &out}, img, opts); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("jpeg.Encode err = %v, want context.DeadlineExceeded", err)
		}
		if out.Len() != 0 {
			t.Fatalf("jpeg.Encode wrote %d bytes on an expired-deadline ctx", out.Len())
		}
	})
}

// --- F1/F2: deterministic wiring pins (encodeSink seam) -------------------

// failingWriter fails every call with a fixed error. It implements the full
// Flush/Write/WriteByte set, so jpeg.Encode's writer-interface fast path uses
// it directly (writer.go:581) and the sticky e.err propagation is
// deterministic: the first write fails, every later write no-ops, and Encode
// returns the error unwrapped (writer.go:640).
type failingWriter struct{ err error }

func (w *failingWriter) Write(p []byte) (int, error) { return 0, w.err }
func (w *failingWriter) WriteByte(c byte) error      { return w.err }
func (w *failingWriter) Flush() error                { return w.err }

// TestGenerateLockedSurfacesEncodeSinkError deterministically pins the F1
// wiring: generateLocked must route jpeg.Encode through the package seam
// encodeSink. The stub records the invocation and returns a writer that
// fails with a sentinel on every call; generateLocked must surface that
// error unwrapped — not reclassified (ErrUnsupported), not wrapped
// (OpenError), not replaced. Pre-seam (jpeg.Encode(&buf, …) with no
// encodeSink), the stub is never invoked and sinkCalled fails the test; a
// later revert to &buf is caught the same way. The A2 byte-identity anchors
// cannot catch a wiring revert (identical bytes) — this is the deterministic
// pin for the only production edit.
func TestGenerateLockedSurfacesEncodeSinkError(t *testing.T) {
	data := makePNG(t, 64, 64)
	sentinel := errors.New("encode sink probe: forced encode failure")
	old := encodeSink
	var sinkCalled bool
	encodeSink = func(context.Context, *bytes.Buffer) io.Writer {
		sinkCalled = true
		return &failingWriter{err: sentinel}
	}
	defer func() { encodeSink = old }()

	_, err := GenerateContext(context.Background(), bytes.NewReader(data), 64, 64)
	if !sinkCalled {
		t.Fatal("encodeSink never invoked — generateLocked is not wired through the seam")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the encode sink's error unwrapped (%v)", err, sentinel)
	}
	if errors.Is(err, ErrUnsupported) || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, must not be reclassified", err)
	}
	assertSlotsReleased(t)
}

// flipCtx is the fault-injection context for the F2 terminal-check pin: Err()
// reports nil until set() flips it; Done() never fires, so
// acquireDecodeSlotContext's select can never park on the ctx branch (the
// slot-send branch always wins — the same shape as semaphore_test.go's
// lateCancelCtx).
type flipCtx struct {
	context.Context
	err error
}

func (c *flipCtx) Done() <-chan struct{} { return nil }
func (c *flipCtx) Err() error            { return c.err }
func (c *flipCtx) set(err error)         { c.err = err }

// flipOnFlushWriter is the stub's pass-through writer for the F2 pin: it
// never consults ctx (so the writer path cannot abort — Encode always returns
// nil), and its final Flush flips the flipCtx to context.Canceled. stdlib
// image/jpeg calls e.w.Flush() exactly once, as the LAST call of Encode
// (writer.go:639-640; writeSOS never flushes), so the flip lands only after
// the encoder's last write: every pre-encode ctx.Err() check in generateLocked
// sees a live context, and the D1 terminal post-encode check is the only
// remaining observer of the flipped error — deterministic, no timing.
type flipOnFlushWriter struct {
	ctx *flipCtx
	buf *bytes.Buffer
}

func (w *flipOnFlushWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *flipOnFlushWriter) WriteByte(c byte) error      { return w.buf.WriteByte(c) }
func (w *flipOnFlushWriter) Flush() error {
	w.ctx.set(context.Canceled)
	return nil
}

// TestGenerateTerminalCheckFiresAfterEncode deterministically pins the D1
// terminal post-encode check (F2): with encodeSink stubbed to a pure
// pass-through writer under a context whose Err() flips only after the
// encoder's last call, the terminal check is the only ctx.Err() observer on
// the success path. The cancel never lands inside Encode (the writer never
// aborts), so the writer path cannot mask a missing terminal check — the
// check itself is what fires: a regression deleting it returns the completed
// JPEG with nil error → red.
func TestGenerateTerminalCheckFiresAfterEncode(t *testing.T) {
	data := makePNG(t, 64, 64)
	ctx := &flipCtx{Context: context.Background()}

	old := encodeSink
	var sinkCalled bool
	encodeSink = func(_ context.Context, buf *bytes.Buffer) io.Writer {
		sinkCalled = true
		return &flipOnFlushWriter{ctx: ctx, buf: buf}
	}
	defer func() { encodeSink = old }()

	_, err := GenerateContext(ctx, bytes.NewReader(data), 64, 64)
	if !sinkCalled {
		t.Fatal("encodeSink never invoked")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled from the terminal post-encode check", err)
	}
	assertSlotsReleased(t)
}
