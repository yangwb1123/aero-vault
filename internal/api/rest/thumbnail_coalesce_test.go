package rest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

type blockingThumbnailStorage struct {
	storage.Storage
	entered    chan struct{}
	release    chan struct{}
	enteredOne sync.Once
	releaseOne sync.Once
}

func (s *blockingThumbnailStorage) GetGenerationBound(
	ctx context.Context, key string, expected storage.ObjectInfo,
) (io.ReadCloser, storage.ObjectInfo, error) {
	s.enteredOne.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, storage.ObjectInfo{}, ctx.Err()
	}
	bound, ok := s.Storage.(storage.GenerationBoundStorage)
	if !ok {
		return nil, storage.ObjectInfo{}, storage.ErrUnsupported
	}
	return bound.GetGenerationBound(ctx, key, expected)
}

func (s *blockingThumbnailStorage) unblock() {
	s.releaseOne.Do(func() { close(s.release) })
}

func newThumbnailCoalesceREST(t *testing.T) (*httptest.Server, *countingStore, *service.FileService, repository.Repository, *blockingThumbnailStorage) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		_ = repo.Close()
		t.Fatalf("migrate: %v", err)
	}
	real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		_ = repo.Close()
		t.Fatalf("storage: %v", err)
	}
	blocked := &blockingThumbnailStorage{
		Storage: real, entered: make(chan struct{}), release: make(chan struct{}),
	}
	store := &countingStore{Storage: blocked}
	svc := service.NewFileService(store, repo, nil).WithDeleteFailOpen(true)
	h := NewHandler(svc, nil).WithThumbnailCache(thumbnailCacheForTest())
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		blocked.unblock()
		srv.Close()
		_ = repo.Close()
	})
	return srv, store, svc, repo, blocked
}

func thumbnailCacheForTest() *thumbnail.Cache {
	return thumbnail.NewCache(1<<20, 0)
}

func TestThumbnailCoalescedJoinerRechecksOpenedGeneration(t *testing.T) {
	srv, store, _, repo, blocked := newThumbnailCoalesceREST(t)
	base := srv.URL + "/v1/files/mutating.png"
	if resp, _ := req(t, http.MethodPut, base, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT A status=%d, want 201", resp.StatusCode)
	}
	row, err := repo.GetObject(context.Background(), "default", "default", "mutating.png")
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	width, height := thumbnail.EffectiveDims(32, 32)
	oldValidator := quotedThumbETag(thumbValidatorETag(thumbnail.CacheKeyVersion,
		thumbnailSourceIdentity(row), row.ETag, width, height))
	thumb := base + "/thumbnail?w=32&h=32"
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			resp, _ := req(t, http.MethodGet, thumb, nil, map[string]string{"If-Match": oldValidator})
			results <- resp.StatusCode
		}()
	}
	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("leader did not reach the blocked source")
	}
	if resp, _ := req(t, http.MethodPut, base, pngBytesAlt(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT B status=%d, want 201", resp.StatusCode)
	}
	blocked.unblock()
	for i := 0; i < 2; i++ {
		select {
		case status := <-results:
			if status != http.StatusPreconditionFailed {
				t.Fatalf("coalesced request %d status=%d, want 412", i, status)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("coalesced mutation request did not finish")
		}
	}
	if store.gets.Load() != 1 {
		t.Fatalf("coalesced mutation storage GETs=%d, want 1", store.gets.Load())
	}
}

func TestThumbnailConcurrentColdBurstCoalescesAndEmitsAccessed(t *testing.T) {
	srv, store, svc, _, blocked := newThumbnailCoalesceREST(t)
	sink := &recordingSink{}
	svc.WithEventSink(sink)
	base := srv.URL + "/v1/files/burst.png"
	thumb := base + "/thumbnail?w=32&h=32"
	putResp, _ := req(t, http.MethodPut, base, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"})
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status=%d, want 201", putResp.StatusCode)
	}

	const callers = 6
	start := make(chan struct{})
	arrived := make(chan struct{})
	var arrivedCount atomic.Int64
	var arrivedOnce sync.Once
	// Gate all burst requests before they enter the handler so the leader
	// cannot finish before the other cold callers have reached the route.
	gate := chi.NewRouter()
	gate.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/thumbnail") {
				if arrivedCount.Add(1) == callers {
					arrivedOnce.Do(func() { close(arrived) })
				}
				<-start
			}
			next.ServeHTTP(w, r)
		})
	})
	gate.Mount("/", srv.Config.Handler.(http.Handler))
	burstSrv := httptest.NewServer(gate)
	defer burstSrv.Close()
	burstURL := strings.Replace(thumb, srv.URL, burstSrv.URL, 1)

	type result struct {
		status int
		body   []byte
		err    error
	}
	results := make(chan result, callers)
	for i := 0; i < callers; i++ {
		go func() {
			resp, err := http.Get(burstURL)
			if err != nil {
				results <- result{err: err}
				return
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			results <- result{status: resp.StatusCode, body: body, err: readErr}
		}()
	}
	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("cold burst did not reach the route barrier")
	}
	close(start)
	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cold burst leader did not open storage")
	}
	blocked.unblock()
	var first []byte
	for i := 0; i < callers; i++ {
		select {
		case got := <-results:
			if got.err != nil || got.status != http.StatusOK || len(got.body) == 0 {
				t.Fatalf("caller %d: status=%d body=%d err=%v", i, got.status, len(got.body), got.err)
			}
			if first == nil {
				first = got.body
			} else if !bytes.Equal(first, got.body) {
				t.Fatalf("caller %d received different thumbnail bytes", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cold burst caller did not finish")
		}
	}
	if store.gets.Load() != 1 {
		t.Fatalf("cold burst storage GETs=%d, want 1", store.gets.Load())
	}
	if got := sink.count(repository.EventAccessed); got != callers {
		t.Fatalf("cold burst EventAccessed=%d, want %d", got, callers)
	}
}
