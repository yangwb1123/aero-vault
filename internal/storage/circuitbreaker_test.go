package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type failStorage struct {
	Storage
	count     int
	threshold int // fail when count < threshold
}

type staticErrorStorage struct {
	Storage
	err error
}

func (s *staticErrorStorage) Stat(context.Context, string) (ObjectInfo, error) {
	return ObjectInfo{}, s.err
}

func (f *failStorage) fail() bool {
	f.count++
	return f.count <= f.threshold
}

func (f *failStorage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
	if f.fail() {
		return ObjectInfo{}, errors.New("injected failure")
	}
	return f.Storage.Put(ctx, key, r, size, opts)
}

func (f *failStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if f.fail() {
		return nil, ObjectInfo{}, errors.New("injected failure")
	}
	return f.Storage.Get(ctx, key)
}

func (f *failStorage) Delete(ctx context.Context, key string) error {
	if f.fail() {
		return errors.New("injected failure")
	}
	return f.Storage.Delete(ctx, key)
}

func newLocal(t *testing.T) Storage {
	t.Helper()
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return s
}

func cbState(t *testing.T, s Storage) CBState {
	t.Helper()
	cb, ok := s.(*circuitBreaker)
	if !ok {
		t.Fatal("wrapper is not a circuitBreaker")
		return CBClosed
	}
	return cb.State()
}

func TestCircuitBreaker_Disabled(t *testing.T) {
	inner := newLocal(t)
	cb := NewCircuitBreaker(inner, CBConfig{Enabled: false})
	if cb != inner {
		t.Fatal("disabled breaker must return inner")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	inner := newLocal(t)
	fs := &failStorage{Storage: inner, threshold: 5}
	cb := NewCircuitBreaker(fs, CBConfig{
		Enabled: true, FailureThreshold: 3, RecoveryTimeout: time.Hour,
	})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := cb.Put(ctx, "k", strings.NewReader("x"), 1, PutOptions{})
		if err == nil {
			t.Fatalf("expected error #%d", i+1)
		}
	}
	if s := cbState(t, cb); s != CBOpen {
		t.Fatalf("expected open, got %v", s)
	}
	_, _, err := cb.Get(ctx, "k")
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("expected ErrBackendUnavailable, got %v", err)
	}
}

func TestCircuitBreaker_Recovers(t *testing.T) {
	inner := newLocal(t)
	fs := &failStorage{Storage: inner, threshold: 2}
	cb := NewCircuitBreaker(fs, CBConfig{
		Enabled: true, FailureThreshold: 2, RecoveryTimeout: 50 * time.Millisecond,
	})
	ctx := context.Background()

	_, _ = cb.Put(ctx, "k1", strings.NewReader("x"), 1, PutOptions{})
	_, _ = cb.Put(ctx, "k1", strings.NewReader("x"), 1, PutOptions{})
	if s := cbState(t, cb); s != CBOpen {
		t.Fatalf("expected open, got %v", s)
	}

	time.Sleep(60 * time.Millisecond)
	if s := cbState(t, cb); s != CBHalfOpen {
		t.Fatalf("expected half-open, got %v", s)
	}

	// threshold=2, so count >= 3 succeeds
	info, err := cb.Put(ctx, "k2", strings.NewReader("hello"), 5, PutOptions{})
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if info.Size != 5 {
		t.Fatalf("size=%d", info.Size)
	}
	if s := cbState(t, cb); s != CBClosed {
		t.Fatalf("expected closed, got %v", s)
	}
}

func TestCircuitBreaker_IgnoresExpectedErrors(t *testing.T) {
	expected := []error{
		ErrNotFound,
		ErrAlreadyExists,
		ErrInvalidKey,
		ErrUnsupported,
		ErrSSECustomerKeyRequired,
		ErrInvalidSSECustomerKey,
		ErrBackendUnavailable,
		context.Canceled,
	}
	for _, want := range expected {
		t.Run(want.Error(), func(t *testing.T) {
			inner := &staticErrorStorage{Storage: newLocal(t), err: want}
			cb := NewCircuitBreaker(inner, CBConfig{
				Enabled: true, FailureThreshold: 1, RecoveryTimeout: time.Hour,
			})
			for i := 0; i < 3; i++ {
				_, err := cb.Stat(context.Background(), "k")
				if !errors.Is(err, want) {
					t.Fatalf("error #%d = %v, want %v", i+1, err, want)
				}
			}
			if s := cbState(t, cb); s != CBClosed {
				t.Fatalf("expected closed, got %v", s)
			}
			_, failures, total := cb.(*circuitBreaker).Stats()
			if failures != 0 {
				t.Fatalf("failures=%d, want 0", failures)
			}
			if total != 3 {
				t.Fatalf("total=%d, want 3", total)
			}
		})
	}
}

func TestCircuitBreaker_ExpectedErrorDoesNotConsumeHalfOpenProbe(t *testing.T) {
	inner := newLocal(t)
	fs := &failStorage{Storage: inner, threshold: 1}
	cb := NewCircuitBreaker(fs, CBConfig{
		Enabled: true, FailureThreshold: 1, RecoveryTimeout: 10 * time.Millisecond,
	})
	ctx := context.Background()

	_, _, err := cb.Get(ctx, "missing")
	if err == nil {
		t.Fatal("expected injected failure")
	}
	time.Sleep(30 * time.Millisecond)
	if s := cbState(t, cb); s != CBHalfOpen {
		t.Fatalf("expected half-open, got %v", s)
	}

	_, _, err = cb.Get(ctx, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound probe result, got %v", err)
	}
	if _, err = cb.Put(ctx, "healthy", strings.NewReader("ok"), 2, PutOptions{}); err != nil {
		t.Fatalf("expected next probe to proceed: %v", err)
	}
	if s := cbState(t, cb); s != CBClosed {
		t.Fatalf("expected closed after successful probe, got %v", s)
	}
}
