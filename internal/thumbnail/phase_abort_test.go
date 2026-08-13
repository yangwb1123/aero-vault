package thumbnail

// Phase-boundary cancellation tests (direction: "Honor context cancellation
// between pipeline phases to release decode slots and memory for dead
// requests"). White-box: the unexported semaphore state (decodeSlots) is
// observable via recoverSlots, and the makePNG fixture is reused.
//
// These tests pin the post-stream contract: once the storage stream has been
// fully consumed, a request whose context is done must abort at the next
// phase boundary (post-config C1, post-decode C2, pre-encode C3) with the
// exact ctx.Err() — never run scale/composite/encode to completion for a dead
// client. The readers below never surface a context error from Read, so the
// abort provably comes from the new phase-boundary checks, not the
// pre-existing stream-error branches.
//
// Determinism discipline: channel handshakes only (close/<- happens-before),
// no sleeps, no TotalAlloc measurements — so no race_enabled skip is needed.

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"sync"
	"testing"
	"time"
)

// phaseGateReader serves data; the read that would serve byte gateAt is
// truncated to serve exactly up to that byte, then closes consumed (once) and
// blocks until release. gateAt == len(data) parks the read that would serve
// the final byte (the stream is provably fully drained when consumed fires);
// gateAt == 33 parks the read that completes the PNG config scan (signature 8
// + IHDR chunk 25, no read-ahead — the suite-verified PNG config boundary).
// It never returns a context error from Read: the decode must succeed so the
// abort comes from the phase-boundary checks, not the pre-existing
// stream-error branches.
type phaseGateReader struct {
	data     []byte
	off      int
	gateAt   int
	consumed chan struct{}
	release  chan struct{}
	signal   sync.Once
}

func (r *phaseGateReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	if r.off < r.gateAt && r.off+len(p) > r.gateAt {
		p = p[:r.gateAt-r.off] // serve only up to the gate byte in this call
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	if r.off >= r.gateAt {
		r.signal.Do(func() { close(r.consumed) })
		<-r.release
	}
	return n, nil
}

// drainReader serves data and closes consumed once, when the read that
// serves the final byte completes, then returns EOF. Unlike phaseGateReader
// it never blocks: the stream drains fully, so the abort for a cancel that
// lands after the drain must come from a phase-boundary check that runs
// after the stream is read.
type drainReader struct {
	data     []byte
	off      int
	consumed chan struct{}
	signal   sync.Once
}

func (r *drainReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	if r.off >= len(r.data) {
		r.signal.Do(func() { close(r.consumed) })
	}
	return n, nil
}

// TestGenerateContextAbortsAfterStreamConsumed is the core AC-1 pin: a cancel
// that lands after the storage stream has been fully read must abort the
// pipeline (post-decode C2, deterministically) and free the decode slot. The
// handshake is fully deterministic — the gate reader parks the decoder on its
// final read, the test cancels while it is parked, and only then releases the
// gate, so cancellation provably precedes decode completion. Pre-fix this
// fails: the pipeline runs to completion and a JPEG comes back.
func TestGenerateContextAbortsAfterStreamConsumed(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		data := makePNG(t, 2048, 2048)
		consumed := make(chan struct{})
		release := make(chan struct{})
		reader := &phaseGateReader{data: data, gateAt: len(data), consumed: consumed, release: release}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			_, err := GenerateContext(ctx, reader, 256, 256)
			done <- err
		}()
		select {
		case <-consumed:
		case <-time.After(5 * time.Second):
			t.Fatal("GenerateContext never drained the stream (gate not reached)")
		}

		// Concurrent live control: a healthy caller started while the dead
		// holder is parked must complete promptly — the holder neither blocks
		// a live caller nor delays slot handoff. Joined before recoverSlots.
		ctrlData := makePNG(t, 64, 64)
		ctrlOut := make(chan []byte, 1)
		ctrlErr := make(chan error, 1)
		go func() {
			out, err := GenerateContext(context.Background(), bytes.NewReader(ctrlData), 64, 64)
			ctrlOut <- out
			ctrlErr <- err
		}()
		select {
		case err := <-ctrlErr:
			if err != nil {
				t.Fatalf("concurrent live GenerateContext failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent live GenerateContext did not complete while the dead holder was parked")
		}
		out := <-ctrlOut
		img, format, derr := image.Decode(bytes.NewReader(out))
		if derr != nil || format != "jpeg" || img == nil {
			t.Fatalf("live control output is not a valid JPEG: format=%q err=%v", format, derr)
		}
		if img.Bounds().Dx() > 64 || img.Bounds().Dy() > 64 {
			t.Fatalf("live control dims %s exceed 64x64", img.Bounds())
		}

		cancel()
		close(release) // cancellation provably precedes decode completion
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("GenerateContext did not abort after post-read cancel")
		}
		recoverSlots(t) // all slots acquirable/releasable: none leaked
	})

	t.Run("control", func(t *testing.T) {
		// Live context: the pipeline must complete with a valid thumbnail —
		// the R4 no-op guarantee for healthy requests.
		data := makePNG(t, 2048, 2048)
		out, err := GenerateContext(context.Background(), bytes.NewReader(data), 256, 256)
		if err != nil {
			t.Fatalf("GenerateContext with live ctx failed: %v", err)
		}
		img, format, derr := image.Decode(bytes.NewReader(out))
		if derr != nil || format != "jpeg" || img == nil {
			t.Fatalf("output is not a decodable JPEG: format=%q err=%v", format, derr)
		}
		if img.Bounds().Dx() > 256 || img.Bounds().Dy() > 256 {
			t.Fatalf("thumbnail dims %s exceed 256x256", img.Bounds())
		}
	})

	t.Run("deadline", func(t *testing.T) {
		// R2 identity for the 504 leg: a server-side deadline that fires
		// must surface as context.DeadlineExceeded, never ErrUnsupported.
		// Deterministic: the deadline provably fires before the gate opens.
		//
		// The drain wait accepts BOTH orderings: the decode may drain the
		// stream and park at the gate before the deadline (the pre-fix
		// path), or the deadline may fire while the payload is still being
		// read and abort the drain at the next codec buffer fill via the
		// context-checking reader (ctx_reader.go) — the post-fix path,
		// which makes "never drained" the expected outcome when serving the
		// 2048² stream outpaces the 50 ms deadline (as under -race). Both
		// arms converge on the same identity and slot-release assertions.
		data := makePNG(t, 2048, 2048)
		consumed := make(chan struct{})
		release := make(chan struct{})
		reader := &phaseGateReader{data: data, gateAt: len(data), consumed: consumed, release: release}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			_, err := GenerateContext(ctx, reader, 256, 256)
			done <- err
		}()
		select {
		case <-consumed:
			// Stream drained and the decoder parked at the gate: the
			// deadline must fire while it is parked.
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("deadline never fired while the decoder was parked")
			}
		case <-ctx.Done():
			// Deadline fired mid-drain (post-fix): the ctxReader aborted
			// the decode at the next codec buffer fill; the gate reader
			// was never parked.
		case <-time.After(5 * time.Second):
			t.Fatal("GenerateContext neither drained the stream nor hit the deadline (gate not reached)")
		}
		close(release) // unblocks a parked final read; no-op if never parked
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want context.DeadlineExceeded", err)
			}
			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("GenerateContext did not abort after the post-read deadline")
		}
		recoverSlots(t)
	})
}

// TestGenerateContextAbortsAtConfigBoundary pins the post-config check (C1):
// a cancel that lands after DecodeConfig succeeded (gateAt == 33 parks the
// read that completes the PNG config scan, no read-ahead) must abort before
// any pixel data is read — the stream is provably not fully decoded
// (reader.off == 33), the test's proxy for no full-frame allocation. Pre-fix
// this fails: without C1 the decode runs and a JPEG comes back.
func TestGenerateContextAbortsAtConfigBoundary(t *testing.T) {
	data := makePNG(t, 2048, 2048)
	consumed := make(chan struct{})
	release := make(chan struct{})
	reader := &phaseGateReader{data: data, gateAt: 33, consumed: consumed, release: release}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, 256, 256)
		done <- err
	}()
	select {
	case <-consumed:
	case <-time.After(5 * time.Second):
		t.Fatal("config scan never reached the gate byte")
	}
	cancel()
	close(release) // cancellation provably precedes the post-config checks
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not abort at the config boundary")
	}
	if reader.off != 33 {
		t.Fatalf("stream consumed %d bytes, want exactly 33 (no pixel data read for a dead request)", reader.off)
	}
	recoverSlots(t)
}

// TestGenerateContextAbortsBeforeEncode pins the post-decode/scale-phase
// abort (C2/C3 as safety nets): with a 4096x4096 source scaled to 2048x2048,
// the scale phase (~100-400 ms) is orders of magnitude longer than the
// signal-to-cancel round trip (~10-100 µs), so a cancel that lands after the
// stream drained aborts at the post-decode check or inside scale within
// cancelCheckRows rows of pixel work (the deterministic mid-scale pin lives
// in cpu_cancel_test.go's gateImage unit tests); C3 remains the pre-encode
// safety net. Pre-fix this fails: the pipeline completes and a JPEG comes
// back. The former residual false-failure direction — a cancel landing after
// the abort point, during encode — is now closed by the in-encode writer
// boundary (encode_writer.go), pinned by TestGenerateContextAbortsMidEncode;
// the remaining timing residual there is bounded by that test's calibration
// margin (see its doc), never a false pass here.
func TestGenerateContextAbortsBeforeEncode(t *testing.T) {
	data := makePNG(t, 4096, 4096)
	consumed := make(chan struct{})
	reader := &drainReader{data: data, consumed: consumed}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, 2048, 2048)
		done <- err
	}()
	select {
	case <-consumed:
	case <-time.After(10 * time.Second):
		t.Fatal("GenerateContext never drained the stream")
	}
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
		t.Fatal("GenerateContext did not abort mid-scale")
	}
	recoverSlots(t)
}
