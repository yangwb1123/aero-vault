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

	"github.com/aero-vault/aero-vault/internal/service"
)

type closeFailingSource struct {
	io.Reader
	closeErr error
	closed   atomic.Int32
	onClose  func()
}

func (s *closeFailingSource) Close() error {
	s.closed.Add(1)
	if s.onClose != nil {
		s.onClose()
	}
	return s.closeErr
}

type blockingCloseSource struct {
	io.Reader
	closeErr error
	started  chan struct{}
	release  <-chan struct{}
	closed   atomic.Int32
	once     sync.Once
}

func (s *blockingCloseSource) Close() error {
	s.closed.Add(1)
	s.once.Do(func() { close(s.started) })
	<-s.release
	return s.closeErr
}

type verifierCloseTracker struct {
	io.ReadCloser
	readCorrupt atomic.Bool
	closeErr    error
}

func (s *verifierCloseTracker) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if errors.Is(err, service.ErrObjectCorrupt) {
		s.readCorrupt.Store(true)
	}
	return n, err
}

func (s *verifierCloseTracker) Close() error {
	s.closeErr = s.ReadCloser.Close()
	return s.closeErr
}

func TestGenerateContextWithOpenerPropagatesCloseError(t *testing.T) {
	closeErr := errors.New("source close failed")
	source := &closeFailingSource{
		Reader:   bytes.NewReader(makePNG(t, 32, 32)),
		closeErr: closeErr,
	}

	img, err := GenerateContextWithOpener(context.Background(), 16, 16, func() (io.ReadCloser, error) {
		return source, nil
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want source close error", err)
	}
	var sourceErr *SourceReadError
	if !errors.As(err, &sourceErr) || sourceErr.Err != closeErr {
		t.Fatalf("error = %v, want SourceReadError wrapping close error", err)
	}
	if len(img) != 0 {
		t.Fatalf("close failure returned %d image bytes, want none", len(img))
	}
	if got := source.closed.Load(); got != 1 {
		t.Fatalf("source closed %d times, want exactly once", got)
	}
	assertSlotsReleased(t)
}

func TestGenerationClearsOpenedETagOnCloseFailure(t *testing.T) {
	closeErr := errors.New("etag close failure")
	source := &closeFailingSource{
		Reader:   bytes.NewReader(makePNG(t, 32, 32)),
		closeErr: closeErr,
	}
	img, etag, err := generateContextWithAdmission(
		context.Background(), 16, 16, nil, "", func() (io.ReadCloser, string, error) {
			return source, etagA, nil
		},
	)
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want close error", err)
	}
	if len(img) != 0 || etag != "" {
		t.Fatalf("result = bytes=%d etag=%q, want empty result", len(img), etag)
	}
	if got := source.closed.Load(); got != 1 {
		t.Fatalf("source closed %d times, want exactly once", got)
	}
	assertSlotsReleased(t)
}

func TestCachedGenerationDoesNotStoreCloseFailure(t *testing.T) {
	cache := NewCache(1<<20, 0)
	closeErr := errors.New("cache source close failed")
	data := makePNG(t, 32, 32)
	var opens atomic.Int32
	var sources []*closeFailingSource
	open := func() (io.ReadCloser, string, error) {
		opens.Add(1)
		source := &closeFailingSource{Reader: bytes.NewReader(data), closeErr: closeErr}
		sources = append(sources, source)
		return source, etagA, nil
	}

	for i := 0; i < 2; i++ {
		img, fromCache, err := GenerateContextWithOpenerCached(
			context.Background(), cache, "tenant-a", etagA, 16, 16, open,
		)
		if !errors.Is(err, closeErr) {
			t.Fatalf("call %d error = %v, want source close error", i+1, err)
		}
		if fromCache || len(img) != 0 {
			t.Fatalf("call %d returned fromCache=%v bytes=%d, want false/0", i+1, fromCache, len(img))
		}
	}
	if got := opens.Load(); got != 2 {
		t.Fatalf("opener called %d times, want 2 after two failed misses", got)
	}
	for i, source := range sources {
		if got := source.closed.Load(); got != 1 {
			t.Fatalf("source %d closed %d times, want exactly once", i+1, got)
		}
	}
	if cache.Len() != 0 || cache.Bytes() != 0 {
		t.Fatalf("close failure populated cache: len=%d bytes=%d", cache.Len(), cache.Bytes())
	}
	assertSlotsReleased(t)
}

func TestCachedCloseKeepsAdmissionReservationsUntilCloseReturns(t *testing.T) {
	admission := NewDecodeAdmission(1)
	cache := NewCache(1<<20, 0)
	closeErr := errors.New("ordered close failure")
	var globalHeld, tenantHeld atomic.Bool
	source := &closeFailingSource{
		Reader:   bytes.NewReader(makePNG(t, 32, 32)),
		closeErr: closeErr,
		onClose: func() {
			globalHeld.Store(len(decodeSlots) > 0)
			admission.mu.Lock()
			state := admission.states["tenant-a"]
			tenantHeld.Store(state != nil && state.active == 1)
			admission.mu.Unlock()
		},
	}
	_, fromCache, err := GenerateContextWithOpenerCachedWithAdmission(
		context.Background(), cache, admission, "tenant-a", etagA, 16, 16,
		func() (io.ReadCloser, string, error) { return source, etagA, nil },
	)
	if !errors.Is(err, closeErr) || fromCache {
		t.Fatalf("generation error=%v fromCache=%v, want close failure miss", err, fromCache)
	}
	if !globalHeld.Load() || !tenantHeld.Load() {
		t.Fatalf("admission released before close: global=%v tenant=%v", globalHeld.Load(), tenantHeld.Load())
	}
	assertSlotsReleased(t)
	release, err := admission.Acquire(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("tenant reservation not released after return: %v", err)
	}
	release()
}

func TestCachedCloseKeepsBlockedReservationsUntilCloseReturns(t *testing.T) {
	admission := NewDecodeAdmission(1)
	cache := NewCache(1<<20, 0)
	closeErr := errors.New("blocked close failure")
	started := make(chan struct{})
	allowClose := make(chan struct{})
	source := &blockingCloseSource{
		Reader:   bytes.NewReader(makePNG(t, 32, 32)),
		closeErr: closeErr,
		started:  started,
		release:  allowClose,
	}
	result := make(chan struct {
		img       []byte
		fromCache bool
		err       error
	}, 1)
	go func() {
		img, fromCache, err := GenerateContextWithOpenerCachedWithAdmission(
			context.Background(), cache, admission, "tenant-a", etagA, 16, 16,
			func() (io.ReadCloser, string, error) { return source, etagA, nil },
		)
		result <- struct {
			img       []byte
			fromCache bool
			err       error
		}{img: img, fromCache: fromCache, err: err}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("source close did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	releaseTenant, err := admission.Acquire(ctx, "tenant-a")
	cancel()
	if err == nil {
		releaseTenant()
		t.Fatal("tenant reservation became available while close was blocked")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("tenant acquire error = %v, want deadline", err)
	}
	for i := 0; i < maxConcurrentDecodes-1; i++ {
		acquireDecodeSlot()
	}
	globalFilled := true
	defer func() {
		if globalFilled {
			for i := 0; i < maxConcurrentDecodes-1; i++ {
				releaseDecodeSlot()
			}
		}
	}()
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = acquireDecodeSlotContext(ctx)
	cancel()
	if err == nil {
		releaseDecodeSlot()
		t.Fatal("global slot became available while close was blocked")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("global acquire error = %v, want deadline", err)
	}

	close(allowClose)
	select {
	case got := <-result:
		if got.fromCache || len(got.img) != 0 || !errors.Is(got.err, closeErr) {
			t.Fatalf("result = fromCache=%v bytes=%d err=%v, want close failure miss", got.fromCache, len(got.img), got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("generation did not finish after close returned")
	}
	if got := source.closed.Load(); got != 1 {
		t.Fatalf("source closed %d times, want exactly once", got)
	}
	for i := 0; i < maxConcurrentDecodes-1; i++ {
		releaseDecodeSlot()
	}
	globalFilled = false
	assertSlotsReleased(t)
	releaseTenant, err = admission.Acquire(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("tenant reservation remained held after return: %v", err)
	}
	releaseTenant()
}

func TestCachedVerifierMismatchPropagatesFromClose(t *testing.T) {
	data := makePNG(t, 32, 32)
	admission := NewDecodeAdmission(1)
	cache := NewCache(1<<20, 0)
	var globalHeld, tenantHeld atomic.Bool
	underlying := &closeFailingSource{
		Reader: bytes.NewReader(data),
		onClose: func() {
			globalHeld.Store(len(decodeSlots) > 0)
			admission.mu.Lock()
			state := admission.states["tenant-a"]
			tenantHeld.Store(state != nil && state.active == 1)
			admission.mu.Unlock()
		},
	}
	verifier := service.NewSamplingETagVerifier(underlying, service.ETagVerifierConfig{
		Expected:   "00000000000000000000000000000000",
		ObjectSize: int64(len(data)),
	})
	tracked := &verifierCloseTracker{ReadCloser: verifier}
	img, fromCache, err := GenerateContextWithOpenerCachedWithAdmission(
		context.Background(), cache, admission, "tenant-a", etagA, 16, 16,
		func() (io.ReadCloser, string, error) { return tracked, etagA, nil },
	)
	if !errors.Is(err, service.ErrObjectCorrupt) {
		t.Fatalf("error = %v, want close-time ErrObjectCorrupt", err)
	}
	if tracked.readCorrupt.Load() {
		t.Fatalf("ErrObjectCorrupt surfaced from Read; expected final verification in Close")
	}
	if !errors.Is(tracked.closeErr, service.ErrObjectCorrupt) {
		t.Fatalf("tracked Close error = %v, want ErrObjectCorrupt", tracked.closeErr)
	}
	if !globalHeld.Load() || !tenantHeld.Load() {
		t.Fatalf("admission released before verifier Close: global=%v tenant=%v", globalHeld.Load(), tenantHeld.Load())
	}
	if fromCache || len(img) != 0 {
		t.Fatalf("result = fromCache=%v bytes=%d, want false/0", fromCache, len(img))
	}
	if got := underlying.closed.Load(); got != 1 {
		t.Fatalf("underlying source closed %d times, want exactly once", got)
	}
	if cache.Len() != 0 || cache.Bytes() != 0 {
		t.Fatalf("verification failure populated cache: len=%d bytes=%d", cache.Len(), cache.Bytes())
	}
	assertSlotsReleased(t)
	release, err := admission.Acquire(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("admission reservation remained held: %v", err)
	}
	release()
}

func TestCloseErrorIsRetainedWithDecodeError(t *testing.T) {
	closeErr := errors.New("close after decode failure")
	source := &closeFailingSource{
		Reader:   bytes.NewReader([]byte("not an image")),
		closeErr: closeErr,
	}
	img, err := GenerateContextWithOpener(context.Background(), 16, 16, func() (io.ReadCloser, error) {
		return source, nil
	})
	if !errors.Is(err, ErrUnsupported) || !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want both ErrUnsupported and close error", err)
	}
	if len(img) != 0 || source.closed.Load() != 1 {
		t.Fatalf("failed generation returned bytes or wrong close count: bytes=%d closes=%d", len(img), source.closed.Load())
	}
	assertSlotsReleased(t)
}
