package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type probeCountingReader struct {
	calls atomic.Int32
}

func (r *probeCountingReader) Read([]byte) (int, error) {
	r.calls.Add(1)
	return 0, errors.New("probe source must not be read")
}

type probeGateReader struct {
	entered     chan struct{}
	release     chan struct{}
	err         error
	once        sync.Once
	releaseOnce sync.Once
	calls       atomic.Int32
}

func (r *probeGateReader) Read([]byte) (int, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return 0, r.err
}

func (r *probeGateReader) releaseGate() {
	r.releaseOnce.Do(func() { close(r.release) })
}

// productionProbeSource is a context-oblivious JPEG source that ends at the
// exact MaxSourceBytes boundary. A Read at that offset is the recorder's
// probe; the production context guard must prevent it from reaching here.
type productionProbeSource struct {
	payload         *boundedSource
	canceled        atomic.Bool
	boundaryRead    atomic.Int32
	afterCancelRead atomic.Int32
}

func (r *productionProbeSource) Read(p []byte) (int, error) {
	if r.payload.off >= MaxSourceBytes {
		r.boundaryRead.Add(1)
	}
	if r.canceled.Load() {
		r.afterCancelRead.Add(1)
	}
	return r.payload.Read(p)
}

// probeBoundaryContext models cancellation immediately after the outer
// guarded reader's pre-delegation check at the limit boundary. Its first Err
// call at that boundary closes Done but returns nil, allowing the outer guard
// to delegate the limit's synthetic EOF; the recorder's independent probe
// guard then observes the exact sentinel without consulting the source.
type probeBoundaryContext struct {
	context.Context
	source      *productionProbeSource
	want        error
	done        chan struct{}
	outerPassed atomic.Bool
	probeChecks atomic.Int32
	cancelOnce  sync.Once
	probeOnce   sync.Once
}

func (c *probeBoundaryContext) Done() <-chan struct{} { return c.done }

func (c *probeBoundaryContext) Err() error {
	if c.source.payload.off < MaxSourceBytes {
		return nil
	}
	if c.outerPassed.CompareAndSwap(false, true) {
		c.cancelOnce.Do(func() { close(c.done) })
		return nil
	}
	c.probeOnce.Do(func() {
		c.probeChecks.Add(1)
		c.source.canceled.Store(true)
	})
	return c.want
}

func assertProbeCancellation(t *testing.T, rec *sourceCapRecorder, want error) {
	t.Helper()
	if rec.probeErr != want {
		t.Fatalf("probeErr = %v (%T), want exact %v (%T)", rec.probeErr, rec.probeErr, want, want)
	}
	if !rec.probed || !rec.capped {
		t.Fatalf("probed=%v capped=%v, want both true", rec.probed, rec.capped)
	}
	if errors.Is(rec.probeErr, ErrSourceTooLarge) {
		t.Fatalf("probeErr = %v, must not become ErrSourceTooLarge", rec.probeErr)
	}
	var sourceErr *SourceReadError
	if errors.As(rec.probeErr, &sourceErr) {
		t.Fatalf("probeErr = %v, must not become SourceReadError", rec.probeErr)
	}
}

func TestGenerateContextGuardsProductionCapProbeCancellation(t *testing.T) {
	base := appnPaddedJPEG(t, 0)
	sos := bytes.Index(base, []byte{0xFF, 0xDA})
	if sos < 0 {
		t.Fatal("no SOS marker in fixture")
	}
	segLen := int(binary.BigEndian.Uint16(base[sos+2 : sos+4]))
	prefix := base[:sos+2+segLen]

	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			source := &productionProbeSource{
				payload: &boundedSource{prefix: prefix, total: MaxSourceBytes},
			}
			ctx := &probeBoundaryContext{
				Context: context.Background(), source: source, want: want,
				done: make(chan struct{}),
			}
			out, err := GenerateContext(ctx, source, 100, 100)
			if err != want {
				t.Fatalf("err = %v (%T), want exact %v (%T)", err, err, want, want)
			}
			if len(out) != 0 {
				t.Fatalf("output length = %d, want zero on cancellation", len(out))
			}
			if source.payload.off != MaxSourceBytes {
				t.Fatalf("source offset = %d, want exactly MaxSourceBytes (%d)", source.payload.off, MaxSourceBytes)
			}
			if got := ctx.probeChecks.Load(); got != 1 {
				t.Fatalf("guarded probe checks = %d, want exactly one", got)
			}
			if got := source.boundaryRead.Load(); got != 0 {
				t.Fatalf("source received %d probe reads at the cap boundary", got)
			}
			if got := source.afterCancelRead.Load(); got != 0 {
				t.Fatalf("source received %d reads after probe cancellation", got)
			}
			if errors.Is(err, ErrSourceTooLarge) {
				t.Fatalf("err = %v, must not be reclassified as ErrSourceTooLarge", err)
			}
			assertSlotsReleased(t)
		})
	}
}

func TestSourceCapRecorderAlreadyCanceledProbe(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			ctx := newCancelTestContext()
			ctx.finish(want)
			source := &probeCountingReader{}
			rec := &sourceCapRecorder{
				r:   bytes.NewReader(nil),
				src: &ctxReader{ctx: ctx, r: &sourceReadMarker{r: source}},
			}
			if n, err := rec.Read(make([]byte, 1)); n != 0 || err != io.EOF {
				t.Fatalf("rec.Read = (%d, %v), want (0, EOF) from the wrapped limit", n, err)
			}
			assertProbeCancellation(t, rec, want)
			if got := source.calls.Load(); got != 0 {
				t.Fatalf("probe source calls = %d, want zero for a canceled context", got)
			}
			if n, err := rec.Read(make([]byte, 1)); n != 0 || err != io.EOF {
				t.Fatalf("second rec.Read = (%d, %v), want (0, EOF)", n, err)
			}
			if got := source.calls.Load(); got != 0 {
				t.Fatalf("second read invoked probe source %d times, want zero", got)
			}
		})
	}
}

func TestSourceCapRecorderInFlightProbeCancellation(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			ctx := newCancelTestContext()
			source := &probeGateReader{
				entered: make(chan struct{}), release: make(chan struct{}), err: want,
			}
			rec := &sourceCapRecorder{
				r:   bytes.NewReader(nil),
				src: &ctxReader{ctx: ctx, r: &sourceReadMarker{r: source}},
			}
			done := make(chan error, 1)
			finished := make(chan struct{})
			var joined atomic.Bool
			t.Cleanup(func() { cleanupGeneration(t, source.releaseGate, finished, &joined) })
			go func() {
				_, err := rec.Read(make([]byte, 1))
				done <- err
				close(finished)
			}()
			select {
			case <-source.entered:
			case <-time.After(phaseCancellationTimeout):
				source.releaseGate()
				t.Fatal("cap probe did not enter its source read")
			}
			ctx.finish(want)
			source.releaseGate()
			select {
			case err := <-done:
				joined.Store(true)
				if err != io.EOF {
					t.Fatalf("rec.Read = %v, want the wrapped limit's EOF", err)
				}
			case <-time.After(phaseCancellationTimeout):
				t.Fatal("in-flight cap probe did not return")
			}
			assertProbeCancellation(t, rec, want)
			if got := source.calls.Load(); got != 1 {
				t.Fatalf("probe source calls = %d, want exactly one", got)
			}
			if n, err := rec.Read(make([]byte, 1)); n != 0 || err != io.EOF {
				t.Fatalf("second rec.Read = (%d, %v), want (0, EOF)", n, err)
			}
			if got := source.calls.Load(); got != 1 {
				t.Fatalf("second read invoked probe source %d times, want one total", got)
			}
		})
	}
}
