package thumbnail

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

// cancelTestContext makes either standard context sentinel deterministic. The
// source readers in these tests do not consult the context themselves; only
// ctxReader should observe the transition.
type cancelTestContext struct {
	context.Context
	done chan struct{}
	once sync.Once
	mu   sync.RWMutex
	err  error
}

func newCancelTestContext() *cancelTestContext {
	return &cancelTestContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *cancelTestContext) Done() <-chan struct{} { return c.done }

func (c *cancelTestContext) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (c *cancelTestContext) finish(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

// phaseCancelReader is deliberately context-oblivious. It blocks one source
// read after a selected byte offset, lets that already-started read complete
// after cancellation, and records any later source call. A raw reader in any
// pipeline phase therefore produces a post-cancellation call instead of
// silently passing this test.
type phaseCancelReader struct {
	data     []byte
	maxRead  int
	blockAt  int
	off      int
	blocked  chan struct{}
	release  chan struct{}
	blockOne sync.Once
	freeOne  sync.Once
	canceled atomic.Bool
	postRead atomic.Int32
}

func (r *phaseCancelReader) Read(p []byte) (int, error) {
	if r.canceled.Load() {
		r.postRead.Add(1)
	}
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	if r.maxRead > 0 && len(p) > r.maxRead {
		p = p[:r.maxRead]
	}
	if r.off >= r.blockAt {
		r.blockOne.Do(func() {
			close(r.blocked)
			<-r.release
		})
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func (r *phaseCancelReader) releaseGate() {
	r.freeOne.Do(func() { close(r.release) })
}

// metadataOverflowGateReader lets the read that crosses the metadata limit
// enter while the context is live, then waits before returning its bytes. The
// tee consequently gets to report ErrMetadataTooLarge even though the test
// cancels while that arbitrary source Read is in progress.
type metadataOverflowGateReader struct {
	r       io.Reader
	seen    int
	blocked chan struct{}
	release chan struct{}
	block   sync.Once
	free    sync.Once
}

func (r *metadataOverflowGateReader) Read(p []byte) (int, error) {
	if len(p) > 4096 {
		p = p[:4096]
	}
	n, err := r.r.Read(p)
	r.seen += n
	if r.seen > MaxMetadataBytes {
		r.block.Do(func() {
			close(r.blocked)
			<-r.release
		})
	}
	return n, err
}

func (r *metadataOverflowGateReader) releaseGate() {
	r.free.Do(func() { close(r.release) })
}

const phaseCancellationTimeout = 5 * time.Second

func waitGeneration(t *testing.T, finished <-chan struct{}) bool {
	t.Helper()
	select {
	case <-finished:
		return true
	case <-time.After(phaseCancellationTimeout):
		return false
	}
}

func cleanupGeneration(t *testing.T, release func(), finished <-chan struct{}, joined *atomic.Bool) {
	t.Helper()
	release()
	if joined.Load() {
		return
	}
	if !waitGeneration(t, finished) {
		t.Errorf("generation goroutine did not exit after the gate was released")
		return
	}
	joined.Store(true)
}

func expandedGIFConfigFixture() []byte {
	prefix := gifConfigPrefix()
	prefix[10] = 0xf7 // 256-entry global color table
	return append(prefix, make([]byte, 256*3)...)
}

// jpegConfigCancellationFixture returns a valid JPEG with a padded APP1
// region and the first SOF0 offset. Blocking immediately before SOF0 keeps
// image.DecodeConfig in its metadata scan, rather than accidentally testing
// the later payload decoder.
func jpegConfigCancellationFixture(t testing.TB) ([]byte, int) {
	t.Helper()
	data := appnPaddedJPEG(t, 64<<10)
	sof := bytes.Index(data, []byte{0xff, 0xc0})
	if sof < 0 {
		t.Fatal("padded JPEG fixture has no SOF0 marker")
	}
	return data, sof
}

func runPhaseCancellation(t *testing.T, data []byte, maxRead, blockAt int, want error) {
	t.Helper()
	ctx := newCancelTestContext()
	reader := &phaseCancelReader{
		data: data, maxRead: maxRead, blockAt: blockAt,
		blocked: make(chan struct{}), release: make(chan struct{}),
	}
	done := make(chan struct {
		out []byte
		err error
	}, 1)
	finished := make(chan struct{})
	var joined atomic.Bool
	t.Cleanup(func() { cleanupGeneration(t, reader.releaseGate, finished, &joined) })
	go func() {
		out, err := GenerateContext(ctx, reader, 64, 64)
		done <- struct {
			out []byte
			err error
		}{out: out, err: err}
		close(finished)
	}()

	select {
	case <-reader.blocked:
	case <-time.After(phaseCancellationTimeout):
		reader.releaseGate()
		t.Fatal("source-read phase did not reach its cancellation gate")
	}
	ctx.finish(want)
	reader.canceled.Store(true)
	reader.releaseGate()

	result := waitPhaseResult(t, done)
	joined.Store(true)
	if result.err != want {
		t.Fatalf("err = %v (%T), want exact %v (%T)", result.err, result.err, want, want)
	}
	if len(result.out) != 0 {
		t.Fatalf("output length = %d, want zero on cancellation", len(result.out))
	}
	if errors.Is(result.err, ErrUnsupported) || errors.Is(result.err, ErrSourceTooLarge) {
		t.Fatalf("err = %v, must not be a decode or source-size sentinel", result.err)
	}
	if got := reader.postRead.Load(); got != 0 {
		t.Fatalf("source received %d post-cancellation reads", got)
	}
	recoverSlots(t)
}

func waitPhaseResult(t *testing.T, done <-chan struct {
	out []byte
	err error
}) struct {
	out []byte
	err error
} {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(phaseCancellationTimeout):
		t.Fatal("canceled generation did not return")
		return struct {
			out []byte
			err error
		}{}
	}
}

func TestGenerateContextGuardsEverySourceReadPhase(t *testing.T) {
	jpeg := makeJPEG(t, 4096, 4096)
	jpegConfig, jpegSOF := jpegConfigCancellationFixture(t)
	phases := []struct {
		name    string
		data    []byte
		maxRead int
		blockAt int
	}{
		{"DecodeConfig GIF", expandedGIFConfigFixture(), 1, 14},
		{"DecodeConfig JPEG", jpegConfig, 1, jpegSOF},
		{"PNG orientation", makePNG(t, 64, 64), 1, 34},
		{"payload PNG", makePNG(t, 64, 64), 1, 41},
		{"payload JPEG", jpeg, 0, 128 << 10},
	}
	for _, phase := range phases {
		for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
			name := phase.name + "/" + want.Error()
			t.Run(name, func(t *testing.T) {
				runPhaseCancellation(t, phase.data, phase.maxRead, phase.blockAt, want)
			})
		}
	}
}

type lifecycleReadCloser struct {
	*phaseCancelReader
	admission *DecodeAdmission
	closed    atomic.Int32
	global    atomic.Bool
	tenant    atomic.Bool
}

func (r *lifecycleReadCloser) Close() error {
	r.closed.Add(1)
	if len(decodeSlots) > 0 {
		r.global.Store(true)
	}
	if r.admission != nil {
		r.admission.mu.Lock()
		state := r.admission.states["tenant-a"]
		r.admission.mu.Unlock()
		if state != nil && state.active > 0 {
			r.tenant.Store(true)
		}
	}
	return nil
}

func runOpenerCancellation(t *testing.T, want error, admission *DecodeAdmission) {
	t.Helper()
	ctx := newCancelTestContext()
	reader := &lifecycleReadCloser{
		phaseCancelReader: &phaseCancelReader{
			data: expandedGIFConfigFixture(), maxRead: 1, blockAt: 14,
			blocked: make(chan struct{}), release: make(chan struct{}),
		},
		admission: admission,
	}
	result := make(chan struct {
		out []byte
		err error
	}, 1)
	finished := make(chan struct{})
	var joined atomic.Bool
	t.Cleanup(func() { cleanupGeneration(t, reader.releaseGate, finished, &joined) })
	open := func() (io.ReadCloser, OpenedSource, error) {
		return reader, OpenedSource{}, nil
	}
	go func() {
		var out []byte
		var err error
		if admission == nil {
			out, err = GenerateContextWithOpener(ctx, 64, 64, func() (io.ReadCloser, error) {
				return reader, nil
			})
		} else {
			out, _, err = generateContextWithAdmission(ctx, 64, 64, admission, "tenant-a", open)
		}
		result <- struct {
			out []byte
			err error
		}{out: out, err: err}
		close(finished)
	}()

	select {
	case <-reader.blocked:
	case <-time.After(phaseCancellationTimeout):
		reader.releaseGate()
		t.Fatal("opener-backed source did not reach its cancellation gate")
	}
	ctx.finish(want)
	reader.canceled.Store(true)
	reader.releaseGate()
	got := waitPhaseResult(t, result)
	joined.Store(true)
	if got.err != want {
		t.Fatalf("err = %v (%T), want exact %v (%T)", got.err, got.err, want, want)
	}
	if len(got.out) != 0 || reader.closed.Load() != 1 {
		t.Fatalf("out=%d close_count=%d, want empty output and one close", len(got.out), reader.closed.Load())
	}
	if !reader.global.Load() {
		t.Fatal("Close did not observe the global decode slot held")
	}
	if admission != nil && !reader.tenant.Load() {
		t.Fatal("Close did not observe the tenant admission held")
	}
	if reader.postRead.Load() != 0 {
		t.Fatalf("source received %d post-cancellation reads", reader.postRead.Load())
	}
	assertSlotsReleased(t)
	if admission != nil {
		release, err := admission.Acquire(context.Background(), "tenant-a")
		if err != nil {
			t.Fatalf("tenant admission was not released: %v", err)
		}
		release()
	}
}

func TestGenerateAndGenerateContextBackgroundAreByteIdentical(t *testing.T) {
	data := makePNG(t, 64, 64)
	got, err := Generate(bytes.NewReader(data), 32, 32)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want, err := GenerateContext(context.Background(), bytes.NewReader(data), 32, 32)
	if err != nil {
		t.Fatalf("GenerateContext: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Generate and GenerateContext(background) returned different bytes")
	}
}

func TestGenerateContextWithOpenerCancellationReleasesResources(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, withAdmission := range []bool{false, true} {
			name := want.Error()
			if withAdmission {
				name += "/tenant-admission"
			}
			t.Run(name, func(t *testing.T) {
				var admission *DecodeAdmission
				if withAdmission {
					admission = NewDecodeAdmission(1)
				}
				runOpenerCancellation(t, want, admission)
			})
		}
	}
}
