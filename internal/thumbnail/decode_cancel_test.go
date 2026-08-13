package thumbnail

// Decode-phase cancellation tests (direction: "Honor context cancellation
// inside the decode phase"). The decode is the pipeline phase that reads the
// payload through a context-checking reader (ctx_reader.go): mid-decode
// cancellation must abort at the next codec buffer fill (≤ 4 KiB over-read)
// instead of running the decode to completion — releasing the decode slot and
// not draining the source stream. White-box: thresholdReader/gateCountReader
// wrap the source stream; the tests observe bytes served (atomic counters)
// and park the decode inside a fill (channel handshake), then cancel / let a
// self-firing deadline expire while it is parked.
//
// Determinism discipline: channel handshakes and counter sampling only — no
// sleeps, no wall-clock latency assertions, no TotalAlloc measurements — so
// no race_enabled skip is needed. The only timing element (the 200 ms
// WithTimeout deadline in the deadline test) is a self-firing deadline that
// provably expires while the decode is parked (time-to-gate ≈ 1–20 ms even
// under -race, ≥ 10× margin); every assertion is byte-count-based and
// machine-independent. The over-read bounds derive the worst case from the
// codec's own read granularity (jpeg/reader.go buf [4096]byte; fill()
// requests ≤ 4094 B), so a regression that over-reads by an extra fill fails
// loudly, never passes at the margin.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingThresholdReader serves data and closes reached once, when a read
// crosses threshold — the AC-1 handshake: the decode is provably deep inside
// the payload when reached fires (threshold ≫ the config-scan read-ahead).
// The counter is atomic so the test goroutine can sample it while the decode
// goroutine reads (race-safe; no raceEnabled skip needed).
// (Distinct from thumbnail_test.go's non-atomic thresholdReader.)
type countingThresholdReader struct {
	data      []byte
	off       int64
	threshold int64
	reached   chan struct{}
	signal    sync.Once
	count     atomic.Int64
}

func (r *countingThresholdReader) Read(p []byte) (int, error) {
	if r.off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += int64(n)
	r.count.Add(int64(n))
	if r.off >= r.threshold {
		r.signal.Do(func() { close(r.reached) })
	}
	return n, nil
}

// gateCountReader serves data; the read that would carry the stream past
// gateAt serves its FULL request (no truncation — the decoder keeps a full
// codec buffer of input after release, so the decode CAN continue past the
// gate), then closes consumed (once) and blocks until release — the decode is
// parked inside a codec fill. The atomic counter makes the not-drained
// guarantee observable.
//
// Non-truncation is load-bearing: a truncated park (serving exactly up to
// gateAt) leaves the codec with only gateAt bytes of input, so after release
// the decoder runs out at the gate and errors there — the decode never issues
// another fill, and the Decode-site ctx check maps the truncation error to
// the context error in BOTH pre- and post-fix (no discriminator). With a full
// buffer, the decoder consumes the buffered bytes and issues the next fill:
// post-fix that fill hits the ctxReader and aborts (reads stop within one
// fill); pre-fix it drains the stream to len(data) and fails the not-drained
// bound loudly.
type gateCountReader struct {
	data     []byte
	off      int
	gateAt   int
	consumed chan struct{}
	release  chan struct{}
	signal   sync.Once
	count    atomic.Int64
}

func (r *gateCountReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	r.count.Add(int64(n))
	if r.off >= r.gateAt {
		r.signal.Do(func() { close(r.consumed) })
		<-r.release
	}
	return n, nil
}

// TestGenerateContextAbortsDecodeReadsOnCancel is the AC-1 pin: a cancel that
// lands deep inside a live JPEG payload (4096² baseline, 828,883 B — decode
// ~0.2–1 s, orders of magnitude beyond the signal-to-cancel round trip) must
// abort the decode at the next codec buffer fill via ctxReader, release the
// decode slot, and NOT drain the source stream. Pre-fix this fails: the
// decode runs to completion and a JPEG comes back (err == nil, c2 ==
// len(data)).
func TestGenerateContextAbortsDecodeReadsOnCancel(t *testing.T) {
	data := makeJPEG(t, 4096, 4096)
	const threshold = 128 << 10 // deep inside the ~823 KiB entropy payload
	reader := &countingThresholdReader{data: data, threshold: threshold, reached: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, 256, 256)
		done <- err
	}()
	select {
	case <-reader.reached: // ≥ threshold served: decode deep inside the payload
	case <-time.After(5 * time.Second):
		t.Fatal("decode never served the threshold bytes (gate not reached)")
	}
	c0 := reader.count.Load()
	cancel()
	c1 := reader.count.Load() // bytes served up to the cancel
	// Handshake invariant, in its exact form: the reached channel closes only
	// after a read has served ≥ threshold bytes (count.Add precedes the
	// threshold check in the same read), so c0 must be ≥ threshold — a
	// regression that fires the handshake early (or drops the counter) fails
	// here rather than passing the drain bounds vacuously.
	if c0 < threshold {
		t.Fatalf("reached fired with c0 = %d bytes served, want ≥ threshold (%d)", c0, threshold)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not abort after mid-decode cancel")
	}
	c2 := reader.count.Load()
	// Over-read past cancel ≤ one codec fill. Worst case = the in-flight
	// fill() read (≤ 4094 B, jpeg/reader.go buf [4096]byte) plus the bytes
	// the codec already buffered (≤ 4096 B) = ≈ 8190 B; the 16 KiB bound is
	// that derived worst case with real margin. Reads beyond it mean a fill
	// slipped through after cancel (a regression to a non-ctx-checking
	// decode), which the not-drained bound below would also catch.
	if over := c2 - c1; over > 16<<10 {
		t.Fatalf("over-read past cancel = %d bytes, want ≤ 16 KiB (one codec fill)", over)
	}
	// Reader not drained: the abort fires mid-stream (~128–192 KiB served of
	// 828,883 B), never runs to len(data) — a regression that drains the
	// stream fails loudly.
	if c2 > threshold+64<<10 || c2 >= int64(len(data))/2 {
		t.Fatalf("reader served %d bytes, want ≤ %d (abort mid-stream, not drained)", c2, threshold+64<<10)
	}
	recoverSlots(t)
}

// TestGenerateContextStopsDecodeReadsOnDeadline is the AC-2 pin (the 504
// leg, PNG codec path — bufio + synchronous decoder.Read IDAT continuity): a
// self-firing deadline that expires while the decode is parked inside a fill
// must abort at the next codec buffer fill via ctxReader with the exact
// DeadlineExceeded, release the slot, and not drain the stream. The park
// (on the read that carries the stream past 512 B — past the 33 B config
// scan + 8 B orientation-walk boundary, deep inside the 16,865 B IDAT
// payload) makes the ordering deterministic: time-to-park ≈ 1–20 ms even
// under -race, ≥ 10× margin against the 200 ms deadline, so the deadline
// provably fires while the decode is parked. Pre-fix this fails: after
// release the decode drains the whole stream (c2 == len(data)).
//
// The fixture is a 2048² PNG (QA F3's "2048² AC-2 leg"): the codec-matrix
// PNG leg is fully preserved while the 4096² encode cost (~6.4 s under
// -race) is cut to ~1.6 s — the park sits inside IDAT at any size, and the
// assertions are byte-count-based, so the smaller fixture weakens nothing.
func TestGenerateContextStopsDecodeReadsOnDeadline(t *testing.T) {
	data := makePNG(t, 2048, 2048) // 16,865 B
	const gateAt = 512             // past config (33) + walk (8); inside IDAT
	consumed := make(chan struct{})
	release := make(chan struct{})
	reader := &gateCountReader{data: data, gateAt: gateAt, consumed: consumed, release: release}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, 256, 256)
		done <- err
	}()
	select {
	case <-consumed: // decode parked inside a fill, a full codec buffer buffered
	case <-time.After(5 * time.Second):
		t.Fatal("decode never reached the gate (not parked inside a fill)")
	}
	select {
	case <-ctx.Done(): // deadline provably fired while the decode was parked
	case <-time.After(5 * time.Second):
		t.Fatal("deadline never fired while the decode was parked")
	}
	c1 := reader.count.Load()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GenerateContext did not abort after the mid-decode deadline")
	}
	c2 := reader.count.Load()
	// Reads stop promptly: after the deadline fires the decode consumes only
	// the buffered codec input; the next fill returns (0, ctx.Err()) from
	// ctxReader without reading the source (in practice c2 == c1, the parked
	// fill's bytes). The 8 KiB bound covers the in-flight fill.
	if over := c2 - c1; over > 8<<10 {
		t.Fatalf("over-read past deadline = %d bytes, want ≤ 8 KiB (one in-flight fill)", over)
	}
	// Not drained: the abort fires mid-stream (≈ 4 KiB served of 16,865 B).
	// Pre-fix the decode drains to len(data) and fails this loudly.
	if c2 >= int64(len(data)) {
		t.Fatalf("reader served %d bytes (= len(data)): the stream was drained, not aborted mid-decode", c2)
	}
	recoverSlots(t)
}

// scriptedRead is one (n, err) result in a scriptedReader's plan.
type scriptedRead struct {
	n   int
	err error
}

// scriptedReader returns a fixed (n, err) plan, one call per entry — the
// deterministic stub for the reader-contract unit tests.
type scriptedReader struct {
	plan  []scriptedRead
	calls int
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if r.calls >= len(r.plan) {
		return 0, io.EOF
	}
	s := r.plan[r.calls]
	r.calls++
	if s.n > len(p) {
		s.n = len(p)
	}
	return s.n, s.err
}

// TestCtxReaderPassThrough pins the pass-through contract (AC-5): while the
// ctx is alive, every Read returns exactly what the underlying reader
// returns — same n, same error instance. The (n>0, err) shape is the JPEG
// fill() swallow risk (a read that returns bytes together with a non-nil
// error must pass through unchanged — never synthesized into (0, err) or
// (n, nil)), and (0, nil) must pass through without any busy-loop synthesis.
func TestCtxReaderPassThrough(t *testing.T) {
	t.Run("bytes-and-eof", func(t *testing.T) {
		r := &ctxReader{ctx: context.Background(), r: bytes.NewReader([]byte("hello"))}
		var p [16]byte
		n, err := r.Read(p[:])
		if err != nil || n != 5 || string(p[:5]) != "hello" {
			t.Fatalf("Read = (%d, %v), want (5, nil) with 'hello'", n, err)
		}
		// Read(nil) and EOF: io.Reader contract pass-through, no synthesis.
		// bytes.Reader at EOF returns (0, io.EOF) even for a nil buffer —
		// the wrapper must return exactly that, unchanged.
		if n, err := r.Read(nil); n != 0 || err != io.EOF {
			t.Fatalf("Read(nil) at EOF = (%d, %v), want (0, io.EOF) pass-through", n, err)
		}
		if n, err := r.Read(p[:]); n != 0 || err != io.EOF {
			t.Fatalf("Read after drain = (%d, %v), want (0, io.EOF)", n, err)
		}
	})
	t.Run("zero-len-no-advance", func(t *testing.T) {
		// A (0, nil) return from the underlying reader must pass through
		// unchanged — the no-busy-loop guarantee (the codecs' ReadFull loops;
		// the wrapper itself never spins).
		under := &scriptedReader{plan: []scriptedRead{{0, nil}, {4, nil}}}
		r := &ctxReader{ctx: context.Background(), r: under}
		var p [16]byte
		if n, err := r.Read(p[:]); n != 0 || err != nil {
			t.Fatalf("first Read = (%d, %v), want (0, nil) pass-through", n, err)
		}
		if n, err := r.Read(p[:]); n != 4 || err != nil {
			t.Fatalf("second Read = (%d, %v), want (4, nil)", n, err)
		}
	})
	t.Run("bytes-with-error", func(t *testing.T) {
		// (n>0, err) passes through with the EXACT error instance (the JPEG
		// fill() swallow risk, jpeg/reader.go:152-169).
		sentinel := errors.New("scripted: bytes with error")
		under := &scriptedReader{plan: []scriptedRead{{3, sentinel}}}
		r := &ctxReader{ctx: context.Background(), r: under}
		var p [16]byte
		n, err := r.Read(p[:])
		if n != 3 || err != sentinel {
			t.Fatalf("Read = (%d, %v), want (3, same sentinel instance)", n, err)
		}
	})
}

// TestCtxReaderAbortsOnDone pins the done-ctx contract (AC-5): once the
// context is done, the next Read returns (0, ctx.Err()) with the EXACT
// instance (==, not just errors.Is) and the underlying reader is not
// consulted (the source stream is not drained) — including Read(nil), which
// must also abort before delegating (the F5 edge, mirroring the writer
// side's Write(nil) pin in encode_cancel_test.go).
func TestCtxReaderAbortsOnDone(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		under := &scriptedReader{plan: []scriptedRead{{5, nil}}}
		r := &ctxReader{ctx: ctx, r: under}
		var p [16]byte
		if n, err := r.Read(p[:]); n != 0 || err != context.Canceled {
			t.Fatalf("Read = (%d, %v), want (0, exact context.Canceled)", n, err)
		}
		// Underlying reader untouched — the source stream is not drained.
		if under.calls != 0 {
			t.Fatalf("underlying reader consulted %d times on a done ctx, want 0", under.calls)
		}
		// Read(nil) on a done ctx aborts too — no len(p) shortcut (F5).
		if n, err := r.Read(nil); n != 0 || err != context.Canceled {
			t.Fatalf("Read(nil) = (%d, %v), want (0, exact context.Canceled)", n, err)
		}
		if under.calls != 0 {
			t.Fatalf("underlying reader consulted %d times by Read(nil) on a done ctx, want 0", under.calls)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		under := &scriptedReader{plan: []scriptedRead{{5, nil}}}
		r := &ctxReader{ctx: ctx, r: under}
		var p [16]byte
		if n, err := r.Read(p[:]); n != 0 || err != context.DeadlineExceeded {
			t.Fatalf("Read = (%d, %v), want (0, exact context.DeadlineExceeded)", n, err)
		}
		if under.calls != 0 {
			t.Fatalf("underlying reader consulted %d times on an expired-deadline ctx, want 0", under.calls)
		}
	})
}
