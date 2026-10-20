package rest

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/server"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// Each request sees the real older row on its first source Stat and the real
// current row at open. No source metadata or storage proof is fabricated.
type thumbnailAccessSourceRepo struct {
	repository.Repository
	older repository.Object
	mu    sync.Mutex
	calls map[string]int
}

func (r *thumbnailAccessSourceRepo) GetObject(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	id := mw.RequestIDFrom(ctx)
	if key == r.older.Key && id != "" {
		r.mu.Lock()
		r.calls[id]++
		first := r.calls[id] == 1
		r.mu.Unlock()
		if first {
			return r.older, nil
		}
	}
	return r.Repository.GetObject(ctx, tenant, bucket, key)
}

// A nil parent Done prevents database/sql from spawning cancellation watchers.
// After the second cache miss, Done is consulted by the follower's flight wait;
// the source remains parked until the test observes this handshake. Exact open
// and Stat counts below independently reject accidental duplicate generation.
type thumbnailAccessWaitContext struct {
	context.Context
	cache   *thumbnail.Cache
	waiting chan struct{}
	once    sync.Once
}

func (c *thumbnailAccessWaitContext) Done() <-chan struct{} {
	_, misses, _, _ := c.cache.Stats()
	if misses >= 2 {
		c.once.Do(func() { close(c.waiting) })
	}
	return c.Context.Done()
}

type thumbnailAccessSourceFixture struct {
	web     *httptest.Server
	repo    *thumbnailAccessSourceRepo
	store   *thumbnailEncodingOpenStore
	svc     *service.FileService
	cache   *thumbnail.Cache
	sink    *recordingSink
	older   repository.Object
	current repository.Object
	jpeg    []byte
	entered chan struct{}
	waiting chan struct{}
	release chan struct{}
	once    sync.Once
}

func newThumbnailAccessSourceFixture(t *testing.T) *thumbnailAccessSourceFixture {
	t.Helper()
	_, base, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	repo := &thumbnailAccessSourceRepo{Repository: base, calls: make(map[string]int)}
	store := &thumbnailEncodingOpenStore{openedContentStore: &openedContentStore{Storage: local}, bound: true}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{}).WithTenantStatusEnforcement()
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	older := putContentSelectionFixture(t, svc, "access-source.png", pngBytes(t, 8, 8), "image/png")
	body := pngBytesAlt(t, 32, 16)
	current := putContentSelectionFixture(t, svc, older.Key, body, "image/png; charset=utf-8")
	repo.older = older
	jpeg, err := thumbnail.Generate(bytes.NewReader(body), 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	f := &thumbnailAccessSourceFixture{repo: repo, store: store, svc: svc, older: older, current: current, jpeg: jpeg,
		cache: thumbnail.NewCache(1<<20, 0), sink: &recordingSink{}, entered: make(chan struct{}), waiting: make(chan struct{}), release: make(chan struct{})}
	var entered sync.Once
	store.onRead = func() { entered.Do(func() { close(f.entered) }); <-f.release }
	svc.WithEventSink(f.sink)
	f.web = httptest.NewServer(thumbnailAccessSourceHandler(t, f))
	t.Cleanup(func() { f.unblock(); f.web.Close() })
	if older.ETag == current.ETag || older.Size == current.Size || older.ContentType == current.ContentType {
		t.Fatal("fixture needs distinguishable source event metadata")
	}
	return f
}

func thumbnailAccessSourceHandler(t *testing.T, f *thumbnailAccessSourceFixture) http.Handler {
	t.Helper()
	registry, err := auth.Parse("reader:default:read+write")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := chi.NewRouter()
	router.Mount("/v1", NewRouter(f.svc, f.repo, nil, nil, nil, nil, registry, logger, false, nil, nil, 0, false,
		func(h *Handler) { h.WithThumbnailCache(f.cache); h.thumbnailTimeout = 0 }))
	chain := server.ApplyMiddleware(router, f.repo, registry, mw.NewRateLimiter(0, 0), &config.Config{}, logger,
		mw.NewConcurrencyLimiter(0).Middleware(), nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") == "thumbnail-follower" {
			// Only this controlled join handshake suppresses cancellation;
			// this fixture does not claim to test transport cancellation.
			probe := &thumbnailAccessWaitContext{Context: context.WithoutCancel(r.Context()), cache: f.cache, waiting: f.waiting}
			r = r.WithContext(probe)
		}
		chain.ServeHTTP(w, r)
	})
}

func (f *thumbnailAccessSourceFixture) unblock() { f.once.Do(func() { close(f.release) }) }

type thumbnailAccessHTTPResult struct {
	response *http.Response
	body     []byte
	err      error
}

func (f *thumbnailAccessSourceFixture) start(method, path, id string, ctx context.Context, headers map[string]string) <-chan thumbnailAccessHTTPResult {
	result := make(chan thumbnailAccessHTTPResult, 1)
	go func() {
		request, err := http.NewRequestWithContext(ctx, method, f.web.URL+path, nil)
		if err != nil {
			result <- thumbnailAccessHTTPResult{err: err}
			return
		}
		request.Header.Set("Authorization", "Bearer reader")
		request.Header.Set("X-Request-ID", id)
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			result <- thumbnailAccessHTTPResult{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		result <- thumbnailAccessHTTPResult{response: response, body: body, err: err}
	}()
	return result
}

func waitThumbnailAccessSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("thumbnail access source handshake timed out")
	}
}

func collectThumbnailAccessResponse(t *testing.T, result <-chan thumbnailAccessHTTPResult) thumbnailAccessHTTPResult {
	t.Helper()
	select {
	case response := <-result:
		if response.err != nil {
			t.Fatal(response.err)
		}
		return response
	case <-time.After(5 * time.Second):
		t.Fatal("thumbnail access source response timed out")
		return thumbnailAccessHTTPResult{}
	}
}

func assertThumbnailAccessSourceEvent(t *testing.T, sink *recordingSink, requestID string, selected repository.Object) {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var events []repository.Event
	for _, event := range sink.events {
		if event.Type == repository.EventAccessed && event.RequestID == requestID {
			events = append(events, event)
		}
	}
	if len(events) != 1 {
		t.Errorf("request %s access attempts=%d, want exactly one", requestID, len(events))
		return
	}
	event := events[0]
	want := map[string]string{"backend": selected.Backend, "size": strconv.FormatInt(selected.Size, 10),
		"etag": selected.ETag, "content_type": selected.ContentType}
	assertThumbnailAccessEventIdentity(t, event, selected)
	for name, value := range want {
		if event.Payload[name] != value {
			t.Errorf("request %s access event describes preflight: %s=%q want opened %q", requestID, name, event.Payload[name], value)
		}
	}
}

func assertThumbnailAccessEventIdentity(t *testing.T, event repository.Event, selected repository.Object) {
	t.Helper()
	if event.ObjectID == nil || *event.ObjectID != selected.ID || event.TenantID != selected.TenantID || event.Bucket != selected.Bucket || event.Key != selected.Key {
		t.Errorf("access event source identity mismatch: got=%+v want=%+v", event, selected)
	}
}

func assertThumbnailAccessSourceResponse(t *testing.T, f *thumbnailAccessSourceFixture, result thumbnailAccessHTTPResult, method string) {
	t.Helper()
	response := result.response
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") != thumbnailEncodingValidator(f.current) || response.Header.Get("X-Version-Id") != f.current.VersionID {
		t.Errorf("response lost opened identity: status=%d headers=%v body=%q", response.StatusCode, response.Header, result.body)
	}
	if method == http.MethodGet && !bytes.Equal(result.body, f.jpeg) {
		t.Error("response is not the opened source JPEG")
	}
	if method == http.MethodHead && len(result.body) != 0 {
		t.Error("HEAD returned a body")
	}
}

func TestThumbnailAccessSourceCoalescedFollowerUsesOpenedObject(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, selector := range []string{"?representation=thumbnail&w=16&h=16", "/thumbnail?w=16&h=16"} {
			t.Run(method+selector, func(t *testing.T) {
				f := newThumbnailAccessSourceFixture(t)
				path := "/v1/files/" + f.current.Key + selector
				leader := f.start(method, path, "thumbnail-leader", context.Background(), nil)
				waitThumbnailAccessSignal(t, f.entered)
				follower := f.start(method, path, "thumbnail-follower", context.Background(), nil)
				waitThumbnailAccessSignal(t, f.waiting)
				f.unblock()
				assertThumbnailAccessSourceResponse(t, f, collectThumbnailAccessResponse(t, leader), method)
				assertThumbnailAccessSourceResponse(t, f, collectThumbnailAccessResponse(t, follower), method)
				assertThumbnailAccessSourceEvent(t, f.sink, "thumbnail-leader", f.current)
				assertThumbnailAccessSourceEvent(t, f.sink, "thumbnail-follower", f.current)
				if f.store.opens.Load() != 1 || f.store.closes.Load() != 1 || f.store.key.Load() != f.current.StorageKey || f.cache.Len() != 0 {
					t.Errorf("coalesced source duplicated/leaked/cached under old key: opens=%d closes=%d key=%v entries=%d", f.store.opens.Load(), f.store.closes.Load(), f.store.key.Load(), f.cache.Len())
				}
				if f.repo.calls["thumbnail-leader"] != 2 || f.repo.calls["thumbnail-follower"] != 1 {
					t.Errorf("unexpected source Stat counts: %v", f.repo.calls)
				}
			})
		}
	}
}
