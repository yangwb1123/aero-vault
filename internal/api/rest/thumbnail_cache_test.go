package rest

// End-to-end tests for the server-side thumbnail cache through the real REST
// handler (router.go getKey dispatches /thumbnail → h.Thumbnail). Uses the
// counting-store wrapper (stallStore pattern) to prove the storage-GET dedup
// claim, the recording-sink to prove exactly-one EventAccessed per 200, and
// mw.Tenant + X-Aero-Tenant for cross-tenant isolation. -short and -race
// friendly.

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// countingStore wraps a local store and counts storage Get invocations —
// the server-side cache's dedup claim is: repeat thumbnail requests must not
// increment the count.
type countingStore struct {
	storage.Storage
	gets atomic.Int64
}

func (s *countingStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets.Add(1)
	return s.Storage.Get(ctx, key)
}

// recordingSink captures published events so the hit-path EventAccessed
// emission can be counted deterministically.
type recordingSink struct {
	mu     sync.Mutex
	events []repository.Event
}

func (r *recordingSink) Publish(_ context.Context, e repository.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingSink) count(typ repository.EventType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// newThumbnailCacheREST returns an httptest server whose router mirrors the
// production REST shape for the thumbnail path: putKey/getKey/Head/deleteKey
// on /v1/files/* plus the bucket-policy routes (authz gate test). withCache
// attaches a 1 MiB server-side thumbnail cache with the given per-entry TTL
// (0 = no expiry); the counting store is always installed so both cached and
// uncached flows are observable. The raw chi router is returned as well so
// tests can wrap it with middleware (e.g. mw.Tenant for cross-tenant
// isolation).
func newThumbnailCacheREST(t *testing.T, withCache bool, ttl time.Duration) (*httptest.Server, *countingStore, *service.FileService, *chi.Mux) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	store := &countingStore{Storage: real}
	svc := service.NewFileService(store, repo, nil).WithDeleteFailOpen(true)
	h := NewHandler(svc, nil)
	if withCache {
		h.WithThumbnailCache(thumbnail.NewCache(1<<20, ttl))
	}
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	r.Delete("/v1/files/*", h.deleteKey)
	r.Put("/v1/buckets/{bucket}/policy", h.PutBucketPolicy)
	r.Delete("/v1/buckets/{bucket}/policy", h.DeleteBucketPolicy)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv, store, svc, r
}

// pngBytesAlt is a second deterministic PNG fixture with different pixels
// from pngBytes: a PUT with these bytes changes the object ETag, which must
// change the cache key and force a miss.
func pngBytesAlt(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(255 - x%256), uint8(255 - y%256), 55, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// TestThumbnailCacheServesRepeatRequestFromCache is T-8, the wiring
// end-to-end: repeat requests are served from the server-side cache (storage
// Get count stays 1), a PUT with different bytes forces a miss (Get == 2),
// and the same sequence without the cache increments Get per request.
func TestThumbnailCacheServesRepeatRequestFromCache(t *testing.T) {
	srv, store, _, _ := newThumbnailCacheREST(t, true, 0)
	base := srv.URL + "/v1/files/img.png"
	thumb := base + "/thumbnail?w=32&h=32"
	hdr := map[string]string{"Content-Type": "image/png"}

	// Upload and first thumbnail: full pipeline, one storage Get.
	resp, _ := req(t, "PUT", base, pngBytes(t, 64, 64), hdr)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status=%d want 201", resp.StatusCode)
	}
	resp, body := req(t, "GET", thumb, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first GET status=%d want 200, body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", ct)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("200 must carry a derived ETag")
	}
	if img, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		t.Fatalf("thumbnail is not a decodable JPEG: %v", err)
	} else if b := img.Bounds(); b.Dx() > 32 || b.Dy() > 32 {
		t.Fatalf("thumbnail dims %dx%d exceed 32x32", b.Dx(), b.Dy())
	}
	if n := store.gets.Load(); n != 1 {
		t.Fatalf("storage Get count after first thumbnail = %d, want 1", n)
	}

	// Second identical GET (fresh client, no If-None-Match): served from the
	// server-side cache — byte-identical body, no new storage Get.
	resp2, body2 := req(t, "GET", thumb, nil, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("repeat GET status=%d want 200, body=%s", resp2.StatusCode, body2)
	}
	if !bytes.Equal(body, body2) {
		t.Fatal("repeat GET body differs from the first (must be byte-identical)")
	}
	if n := store.gets.Load(); n != 1 {
		t.Fatalf("storage Get count after repeat = %d, want 1 (served from server-side cache)", n)
	}

	// Simulated PUT with different bytes: new ETag → new key → miss → fresh
	// bytes and one more storage Get. Stale bytes are never served.
	resp, _ = req(t, "PUT", base, pngBytesAlt(t, 64, 64), hdr)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-PUT status=%d want 201", resp.StatusCode)
	}
	resp3, body3 := req(t, "GET", thumb, nil, nil)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("GET after re-PUT status=%d want 200", resp3.StatusCode)
	}
	if bytes.Equal(body, body3) {
		t.Fatal("GET after re-PUT must serve the new version's bytes")
	}
	if n := store.gets.Load(); n != 2 {
		t.Fatalf("storage Get count after re-PUT = %d, want 2", n)
	}

	// Regression: without the cache, every request runs the full pipeline.
	srv2, store2, _, _ := newThumbnailCacheREST(t, false, 0)
	base2 := srv2.URL + "/v1/files/img.png"
	thumb2 := base2 + "/thumbnail?w=32&h=32"
	req(t, "PUT", base2, pngBytes(t, 64, 64), hdr)
	req(t, "GET", thumb2, nil, nil)
	req(t, "GET", thumb2, nil, nil)
	if n := store2.gets.Load(); n != 2 {
		t.Fatalf("uncached storage Get count = %d, want 2 (every request opens)", n)
	}
}

// TestThumbnailCacheAuthzGateBeforeLookup pins gate finding #5: cache lookup
// sits strictly after bucket-policy authorization — a denied request 403s
// before the entry point, neither serving nor populating the cache, and a
// subsequent authorized request still hits the surviving entry.
func TestThumbnailCacheAuthzGateBeforeLookup(t *testing.T) {
	srv, store, _, _ := newThumbnailCacheREST(t, true, 0)
	base := srv.URL + "/v1/files/img.png"
	thumb := base + "/thumbnail?w=32&h=32"
	policyURL := srv.URL + "/v1/buckets/default/policy"
	hdr := map[string]string{"Content-Type": "image/png"}

	req(t, "PUT", base, pngBytes(t, 64, 64), hdr)
	resp, body := req(t, "GET", thumb, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("warm-up GET status=%d want 200, body=%s", resp.StatusCode, body)
	}
	if n := store.gets.Load(); n != 1 {
		t.Fatalf("warm-up storage Get count = %d, want 1", n)
	}

	// Deny s3:GetObject: the thumbnail derivation path must 403 before any
	// cache lookup (neither serve nor populate).
	denyGetPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:PutObject","Resource":"arn:aws:s3:::default/*"},{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/*"}]}`
	resp, body = req(t, "PUT", policyURL, bodyPolicy(denyGetPolicy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set deny policy status=%d want 200, body=%s", resp.StatusCode, body)
	}
	resp, body = req(t, "GET", thumb, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied GET status=%d want 403, body=%s", resp.StatusCode, body)
	}
	if n := store.gets.Load(); n != 1 {
		t.Fatalf("denied request must not touch storage: Get count = %d, want 1", n)
	}

	// Restore: the warm entry survived the denied request and serves a hit.
	resp, _ = req(t, "DELETE", policyURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy status=%d want 200", resp.StatusCode)
	}
	resp, body = req(t, "GET", thumb, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restored GET status=%d want 200, body=%s", resp.StatusCode, body)
	}
	if n := store.gets.Load(); n != 1 {
		t.Fatalf("restored GET must hit the surviving entry: Get count = %d, want 1", n)
	}

	// A new version forces a miss (proving the cache is live, not bypassed).
	req(t, "PUT", base, pngBytesAlt(t, 64, 64), hdr)
	resp, body = req(t, "GET", thumb, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after re-PUT status=%d want 200, body=%s", resp.StatusCode, body)
	}
	if n := store.gets.Load(); n != 2 {
		t.Fatalf("re-PUT must force a miss: Get count = %d, want 2", n)
	}
}

// TestThumbnailCacheHitKeeps304AndHeaders pins gate finding #6: after a warm
// hit, an If-None-Match request still returns 304 with mirrored
// Cache-Control + Last-Modified and no storage Get (the 304 re-Stat is
// repo-only).
func TestThumbnailCacheHitKeeps304AndHeaders(t *testing.T) {
	srv, store, _, _ := newThumbnailCacheREST(t, true, 0)
	base := srv.URL + "/v1/files/img.png"
	thumb := base + "/thumbnail?w=32&h=32"
	hdr := map[string]string{"Content-Type": "image/png"}

	req(t, "PUT", base, pngBytes(t, 64, 64), hdr)
	resp, _ := req(t, "GET", thumb, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("warm GET status=%d want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	cc := resp.Header.Get("Cache-Control")
	if etag == "" || cc == "" {
		t.Fatalf("200 headers incomplete: ETag=%q Cache-Control=%q", etag, cc)
	}

	// 304 with the derived validator: mirrored Cache-Control, Last-Modified
	// set (the 304 handler derives it from a fresh re-Stat), no storage Get.
	resp, body := req(t, "GET", thumb, nil, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("304 request status=%d want 304, body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != cc {
		t.Fatalf("304 Cache-Control = %q, want mirrored %q", got, cc)
	}
	if got := resp.Header.Get("Last-Modified"); got == "" {
		t.Fatal("304 must carry Last-Modified")
	}
	if n := store.gets.Load(); n != 1 {
		t.Fatalf("304 must not open a stream: Get count = %d, want 1", n)
	}
}

// TestThumbnailCacheHitEmitsAccessedEvent pins gate C4 (EventAccessed
// parity): every successful 200 emits exactly one EventAccessed — misses via
// stream open in the service, hits via the handler-side EmitAccessed — and
// 304s emit none.
func TestThumbnailCacheHitEmitsAccessedEvent(t *testing.T) {
	srv, _, svc, _ := newThumbnailCacheREST(t, true, 0)
	sink := &recordingSink{}
	svc.WithEventSink(sink)
	base := srv.URL + "/v1/files/img.png"
	thumb := base + "/thumbnail?w=32&h=32"
	hdr := map[string]string{"Content-Type": "image/png"}

	req(t, "PUT", base, pngBytes(t, 64, 64), hdr) // EventCreated — not counted
	resp, _ := req(t, "GET", thumb, nil, nil)     // miss → 1 EventAccessed (stream open)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("miss GET status=%d want 200", resp.StatusCode)
	}
	if n := sink.count(repository.EventAccessed); n != 1 {
		t.Fatalf("miss GET emitted %d EventAccessed, want exactly 1", n)
	}
	resp, _ = req(t, "GET", thumb, nil, nil) // hit → +1 (handler-side EmitAccessed)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hit GET status=%d want 200", resp.StatusCode)
	}
	if n := sink.count(repository.EventAccessed); n != 2 {
		t.Fatalf("hit GET emitted %d EventAccessed total, want 2 (no double emission)", n)
	}
	etag := resp.Header.Get("ETag")
	resp, _ = req(t, "GET", thumb, nil, map[string]string{"If-None-Match": etag}) // 304 → none
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("304 request status=%d want 304", resp.StatusCode)
	}
	if n := sink.count(repository.EventAccessed); n != 2 {
		t.Fatalf("304 emitted %d EventAccessed, want 2 (unchanged)", n)
	}
}

// TestThumbnailCacheCrossTenantIsolationHTTP pins REQ-3 end-to-end: the same
// object bytes and key in two tenants each open exactly once — tenant A's
// cached bytes are never served to tenant B's requests (the tenant component
// of the key). Builds its own server wrapped in mw.Tenant (the production
// chain extracts X-Aero-Tenant before the handler runs).
func TestThumbnailCacheCrossTenantIsolationHTTP(t *testing.T) {
	_, store, _, r := newThumbnailCacheREST(t, true, 0)
	// Wrap the raw router with the tenant middleware (the production chain
	// extracts X-Aero-Tenant before the handler runs) and serve it fresh.
	srv := httptest.NewServer(mw.Tenant(r))
	defer srv.Close()

	put := func(tenant string) {
		rq, _ := http.NewRequest("PUT", srv.URL+"/v1/files/img.png", bytes.NewReader(pngBytes(t, 64, 64)))
		rq.Header.Set("Content-Type", "image/png")
		rq.Header.Set(mw.TenantHeader, tenant)
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatalf("tenant %s PUT: %v", tenant, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("tenant %s PUT status=%d want 201", tenant, resp.StatusCode)
		}
	}
	get := func(tenant string) int {
		rq, _ := http.NewRequest("GET", srv.URL+"/v1/files/img.png/thumbnail?w=32&h=32", nil)
		rq.Header.Set(mw.TenantHeader, tenant)
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatalf("tenant %s GET: %v", tenant, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("tenant %s GET status=%d want 200", tenant, resp.StatusCode)
		}
		return int(store.gets.Load())
	}

	put("tenant-a")
	put("tenant-b")
	if n := get("tenant-a"); n != 1 {
		t.Fatalf("tenant-a first GET: Get count = %d, want 1", n)
	}
	if n := get("tenant-a"); n != 1 {
		t.Fatalf("tenant-a repeat GET: Get count = %d, want 1 (hit)", n)
	}
	if n := get("tenant-b"); n != 2 {
		t.Fatalf("tenant-b first GET: Get count = %d, want 2 (must miss: cross-tenant isolation)", n)
	}
	if n := get("tenant-b"); n != 2 {
		t.Fatalf("tenant-b repeat GET: Get count = %d, want 2 (hit)", n)
	}
}

// BenchmarkThumbnailCacheHandlerHit documents the HTTP hit path: after one
// warm-up request, repeats are served from the cache with no storage GET and
// no decode. Documentation-quality (repo bench discipline — never asserted).
func BenchmarkThumbnailCacheHandlerHit(b *testing.B) {
	dir := b.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(context.Background()); err != nil {
		b.Fatal(err)
	}
	real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		b.Fatal(err)
	}
	store := &countingStore{Storage: real}
	svc := service.NewFileService(store, repo, nil).WithDeleteFailOpen(true)
	h := NewHandler(svc, nil)
	h.WithThumbnailCache(thumbnail.NewCache(1<<20, 0))
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	defer srv.Close()

	base := srv.URL + "/v1/files/img.png"
	thumb := base + "/thumbnail?w=32&h=32"
	benchReq := func(method, url string, body []byte, hdr map[string]string) *http.Response {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		rq, _ := http.NewRequest(method, url, rd)
		for k, v := range hdr {
			rq.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}
	if resp := benchReq("PUT", base, pngBytes(b, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		b.Fatalf("PUT status=%d", resp.StatusCode)
	}
	if resp := benchReq("GET", thumb, nil, nil); resp.StatusCode != http.StatusOK {
		b.Fatalf("warm GET status=%d", resp.StatusCode)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if resp := benchReq("GET", thumb, nil, nil); resp.StatusCode != http.StatusOK {
			b.Fatalf("hit GET status=%d", resp.StatusCode)
		}
	}
}

// TestThumbnailCacheAdmissionGates pins the two cache-bypass gates: SSE-C
// objects and multipart (non-content-derived) ETags must never be served
// from or stored into the server-side cache — every request runs the full
// pipeline (storage Get increments each time), because caching could either
// persist customer-key-decrypted-derived bytes beyond the request (SSE-C)
// or pair stale bytes with a legitimately changed object (multipart ETags
// are not content-derived).
func TestThumbnailCacheAdmissionGates(t *testing.T) {
	newHarness := func(t *testing.T) (*httptest.Server, *countingStore, repository.Repository) {
		t.Helper()
		dir := t.TempDir()
		repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if err := repo.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
		if err != nil {
			t.Fatalf("storage: %v", err)
		}
		store := &countingStore{Storage: real}
		svc := service.NewFileService(store, repo, nil).WithDeleteFailOpen(true)
		h := NewHandler(svc, nil)
		h.WithThumbnailCache(thumbnail.NewCache(1<<20, 0))
		r := chi.NewRouter()
		r.Put("/v1/files/*", h.putKey)
		r.Get("/v1/files/*", h.getKey)
		srv := httptest.NewServer(r)
		t.Cleanup(func() { srv.Close(); _ = repo.Close() })
		return srv, store, repo
	}

	t.Run("SSE-C never cached", func(t *testing.T) {
		srv, store, repo := newHarness(t)
		base := srv.URL + "/v1/files/sec.png"
		thumb := base + "/thumbnail?w=32&h=32"
		hdr := map[string]string{"Content-Type": "image/png"}
		if resp, _ := req(t, "PUT", base, pngBytes(t, 64, 64), hdr); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		// Mark the object as SSE-C (reserved metadata, as prepareSSECWrite
		// would): algorithm + key MD5 present.
		ctx := context.Background()
		for k, v := range map[string]string{
			"_aero_sse_c_algorithm": "AES256",
			"_aero_sse_c_key_md5":   "d41d8cd98f00b204e9800998ecf8427e",
		} {
			if err := repo.SetObjectMetaKey(ctx, "default", "default", "sec.png", k, v); err != nil {
				t.Fatalf("set ssec meta %s: %v", k, err)
			}
		}
		// The guard predicate: SSECustomerInfo must report the object as
		// SSE-C, which is exactly the handler's cache-bypass condition.
		if _, _, ok := service.SSECustomerInfo(objMeta(t, repo, "sec.png")); !ok {
			t.Fatal("SSECustomerInfo must report ok for the seeded SSE-C metadata")
		}
		// Observable contract on the current surface: an SSE-C object has no
		// REST read path without the customer key, so every thumbnail request
		// fails identically (400) — never a cached 200 — and the storage is
		// never opened (the service rejects pre-open). The handler-level
		// cache guard (cache=nil when SSECustomerInfo ok) is defense-in-depth
		// for a future key-passing surface; the consistent failure pins that
		// no derived bytes are ever served or stored for SSE-C objects.
		for i := 0; i < 3; i++ {
			resp, _ := req(t, "GET", thumb, nil, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("GET %d: %d want 400 (SSE-C object unreadable without the customer key)", i, resp.StatusCode)
			}
		}
		if n := store.gets.Load(); n != 0 {
			t.Fatalf("SSE-C storage Get count = %d, want 0 (rejected before any open)", n)
		}
	})

	t.Run("multipart ETag never cached", func(t *testing.T) {
		srv, store, repo := newHarness(t)
		base := srv.URL + "/v1/files/mp.png"
		thumb := base + "/thumbnail?w=32&h=32"
		hdr := map[string]string{"Content-Type": "image/png"}
		if resp, _ := req(t, "PUT", base, pngBytes(t, 64, 64), hdr); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		// Forge a multipart-shaped ETag ("<md5>-<n>", not content-derived)
		// by rewriting the object row's ETag column (the multipart completion
		// path stores exactly this shape).
		obj, err := repo.GetObject(context.Background(), "default", "default", "mp.png")
		if err != nil {
			t.Fatalf("get object: %v", err)
		}
		obj.ETag = "abc123def4567890abcdef1234567890-3"
		if _, err := repo.UpsertObject(context.Background(), obj); err != nil {
			t.Fatalf("rewrite etag: %v", err)
		}
		for i := 0; i < 3; i++ {
			resp, _ := req(t, "GET", thumb, nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %d: %d", i, resp.StatusCode)
			}
		}
		if n := store.gets.Load(); n != 3 {
			t.Fatalf("multipart storage Get count = %d, want 3 (never cached)", n)
		}
	})
}

// TestThumbnailCacheTTLExpiryRegenerates (AC-3) pins the retention contract
// end-to-end: (1) delete arm — after the object is deleted a repeat request
// 404s via the handler's Stat gate (retained bytes are never served), and
// (2) TTL arm — an entry read past its per-entry TTL regenerates (a fresh
// storage Get, byte-identical body) instead of serving retained bytes.
func TestThumbnailCacheTTLExpiryRegenerates(t *testing.T) {
	hdr := map[string]string{"Content-Type": "image/png"}

	t.Run("delete arm: retained bytes never served after object delete", func(t *testing.T) {
		srv, store, _, _ := newThumbnailCacheREST(t, true, 0) // ttl=0: retention is LRU-budget-only
		base := srv.URL + "/v1/files/img.png"
		thumb := base + "/thumbnail?w=32&h=32"

		req(t, "PUT", base, pngBytes(t, 64, 64), hdr)
		resp, body := req(t, "GET", thumb, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("warm GET status=%d want 200, body=%s", resp.StatusCode, body)
		}
		if n := store.gets.Load(); n != 1 {
			t.Fatalf("warm GET storage Get count = %d, want 1 (cache warm)", n)
		}
		// Soft-delete the object: the cache still holds the derived bytes,
		// but the handler's Stat gate fires before any lookup — the repeat
		// request must 404 with no storage Get (the 404 comes from the repo
		// Stat, not storage), never serving the retained body.
		resp, body = req(t, "DELETE", base, nil, nil)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("DELETE status=%d, body=%s", resp.StatusCode, body)
		}
		resp, body = req(t, "GET", thumb, nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET after delete status=%d want 404 (Stat gate before cache lookup), body=%s", resp.StatusCode, body)
		}
		if n := store.gets.Load(); n != 1 {
			t.Fatalf("GET after delete must not open storage: Get count = %d, want 1", n)
		}
	})

	t.Run("TTL arm: expired entry regenerates with byte-identical output", func(t *testing.T) {
		srv, store, _, _ := newThumbnailCacheREST(t, true, time.Nanosecond)
		base := srv.URL + "/v1/files/img.png"
		thumb := base + "/thumbnail?w=32&h=32"

		req(t, "PUT", base, pngBytes(t, 64, 64), hdr)
		resp, body := req(t, "GET", thumb, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("warm GET status=%d want 200, body=%s", resp.StatusCode, body)
		}
		if n := store.gets.Load(); n != 1 {
			t.Fatalf("warm GET storage Get count = %d, want 1", n)
		}
		time.Sleep(2 * time.Millisecond) // nanosecond TTL has elapsed
		resp2, body2 := req(t, "GET", thumb, nil, nil)
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("repeat GET status=%d want 200 (regenerated), body=%s", resp2.StatusCode, body2)
		}
		if n := store.gets.Load(); n != 2 {
			t.Fatalf("repeat GET after TTL expiry must regenerate: storage Get count = %d, want 2 (not served from the expired entry)", n)
		}
		if !bytes.Equal(body, body2) {
			t.Fatal("regenerated body differs from the first — deterministic pipeline must produce identical bytes")
		}
	})
}

// objMeta fetches the live object row's metadata (for the SSE-C predicate
// assertion).
func objMeta(t *testing.T, repo repository.Repository, key string) map[string]string {
	t.Helper()
	obj, err := repo.GetObject(context.Background(), "default", "default", key)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	return obj.Metadata
}
