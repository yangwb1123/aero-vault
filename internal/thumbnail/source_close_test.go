package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
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

func TestCachedGenerationDoesNotStoreCloseFailure(t *testing.T) {
	cache := NewCache(1<<20, 0)
	closeErr := errors.New("cache source close failed")
	data := makePNG(t, 32, 32)
	var opens atomic.Int32
	open := func() (io.ReadCloser, string, error) {
		opens.Add(1)
		return &closeFailingSource{Reader: bytes.NewReader(data), closeErr: closeErr}, etagA, nil
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
