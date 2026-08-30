package rest

import (
	"bytes"
	"context"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

type lateCancelRequestContext struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
	once sync.Once
}

func newLateCancelRequestContext() *lateCancelRequestContext {
	return &lateCancelRequestContext{done: make(chan struct{})}
}

func (c *lateCancelRequestContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *lateCancelRequestContext) Done() <-chan struct{}       { return c.done }

func (c *lateCancelRequestContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *lateCancelRequestContext) Value(any) any { return nil }

func (c *lateCancelRequestContext) trip(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

type lateCancelThumbnailStore struct {
	storage.Storage
	gets      atomic.Int64
	closeOnce sync.Once
	onClose   func()
}

func (s *lateCancelThumbnailStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets.Add(1)
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	return s.wrap(rc), info, nil
}

func (s *lateCancelThumbnailStore) GetGenerationBound(
	ctx context.Context, key string, expected storage.ObjectInfo,
) (io.ReadCloser, storage.ObjectInfo, error) {
	bound, ok := s.Storage.(storage.GenerationBoundStorage)
	if !ok {
		return nil, storage.ObjectInfo{}, storage.ErrUnsupported
	}
	rc, info, err := bound.GetGenerationBound(ctx, key, expected)
	if rc != nil || err == nil {
		s.gets.Add(1)
	}
	if err != nil || rc == nil {
		return rc, info, err
	}
	return s.wrap(rc), info, nil
}

func (s *lateCancelThumbnailStore) wrap(rc io.ReadCloser) io.ReadCloser {
	return &lateCancelReadCloser{ReadCloser: rc, onClose: func() {
		s.closeOnce.Do(func() {
			if s.onClose != nil {
				s.onClose()
			}
		})
	}}
}

type lateCancelReadCloser struct {
	io.ReadCloser
	onClose func()
}

func (r *lateCancelReadCloser) Close() error {
	if r.onClose != nil {
		r.onClose()
	}
	return r.ReadCloser.Close()
}

func TestThumbnailLateCanceledRequestDoesNotWarmCache(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "late-cancel.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	realStore, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	store := &lateCancelThumbnailStore{Storage: realStore}
	h := NewHandler(service.NewFileService(store, repo, nil).WithDeleteFailOpen(true), nil).
		WithThumbnailCache(thumbnail.NewCache(1<<20, 0))
	h.thumbnailTimeout = 0
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)

	putReq := httptest.NewRequest(http.MethodPut, "/v1/files/img.png", bytes.NewReader(pngBytes(t, 64, 64)))
	putReq.Header.Set("Content-Type", "image/png")
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusCreated {
		t.Fatalf("PUT status=%d want 201", putRec.Code)
	}

	lateCtx := newLateCancelRequestContext()
	store.onClose = func() { lateCtx.trip(context.Canceled) }
	thumbPath := "/v1/files/img.png/thumbnail?w=32&h=32"
	firstReq := httptest.NewRequest(http.MethodGet, thumbPath, nil).WithContext(lateCtx)
	firstRec := httptest.NewRecorder()
	r.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("late-canceled request status=%d want recorder default 200", firstRec.Code)
	}
	if firstRec.Body.Len() != 0 {
		t.Fatalf("late-canceled request wrote %d bytes, want 0", firstRec.Body.Len())
	}
	for _, header := range []string{"Content-Type", "ETag", "Content-Length", "Cache-Control", "Last-Modified"} {
		if got := firstRec.Header().Get(header); got != "" {
			t.Fatalf("late-canceled request wrote %s %q", header, got)
		}
	}
	if store.gets.Load() != 1 {
		t.Fatalf("storage gets after late-canceled request=%d want 1", store.gets.Load())
	}
	if h.thumbnailCache.Len() != 0 || h.thumbnailCache.Bytes() != 0 {
		t.Fatalf("late-canceled request warmed cache: len=%d bytes=%d", h.thumbnailCache.Len(), h.thumbnailCache.Bytes())
	}

	secondRec := httptest.NewRecorder()
	r.ServeHTTP(secondRec, httptest.NewRequest(http.MethodGet, thumbPath, nil))
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second request status=%d want 200 (body=%q)", secondRec.Code, secondRec.Body.Bytes())
	}
	if secondRec.Header().Get("ETag") == "" {
		t.Fatal("second request missing ETag")
	}
	if _, err := jpeg.Decode(bytes.NewReader(secondRec.Body.Bytes())); err != nil {
		t.Fatalf("second request returned non-jpeg body: %v", err)
	}
	if store.gets.Load() != 2 {
		t.Fatalf("storage gets after second request=%d want 2 (late-canceled first request must not warm cache)", store.gets.Load())
	}
	if h.thumbnailCache.Len() != 1 || h.thumbnailCache.Bytes() == 0 {
		t.Fatalf("second request did not populate cache: len=%d bytes=%d", h.thumbnailCache.Len(), h.thumbnailCache.Bytes())
	}

	thirdRec := httptest.NewRecorder()
	r.ServeHTTP(thirdRec, httptest.NewRequest(http.MethodGet, thumbPath, nil))
	if thirdRec.Code != http.StatusOK {
		t.Fatalf("third request status=%d want 200", thirdRec.Code)
	}
	if !bytes.Equal(secondRec.Body.Bytes(), thirdRec.Body.Bytes()) {
		t.Fatal("third request body differs from cached second response")
	}
	if store.gets.Load() != 2 {
		t.Fatalf("storage gets after third request=%d want cached hit to stay at 2", store.gets.Load())
	}
}
