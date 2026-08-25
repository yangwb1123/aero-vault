package rest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

const forcedThumbnailETag = "0123456789abcdef0123456789abcdef"

type unsupportedThumbnailStore struct{ storage.Storage }

type generationMismatchThumbnailStore struct{ storage.Storage }

type forcedThumbnailETagStore struct {
	storage.Storage
	etag string
}

func (s *generationMismatchThumbnailStore) GetGenerationBound(ctx context.Context, key string, _ storage.ObjectInfo) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	info.ETag = "contradictory-proof"
	return rc, info, nil
}

func (s *forcedThumbnailETagStore) Put(ctx context.Context, key string, r io.Reader, size int64, opts storage.PutOptions) (storage.ObjectInfo, error) {
	info, err := s.Storage.Put(ctx, key, r, size, opts)
	if err == nil {
		info.ETag = s.etag
	}
	return info, err
}

func (s *forcedThumbnailETagStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err == nil {
		info.ETag = s.etag
	}
	return rc, info, err
}

func (s *forcedThumbnailETagStore) GetGenerationBound(ctx context.Context, key string, expected storage.ObjectInfo) (io.ReadCloser, storage.ObjectInfo, error) {
	bound, ok := s.Storage.(storage.GenerationBoundStorage)
	if !ok {
		return nil, storage.ObjectInfo{}, storage.ErrUnsupported
	}
	actual, err := s.Storage.Stat(ctx, key)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	// The wrapper models a provider whose whole-object ETag is the forced
	// value while retaining the local sidecar generation proof underneath.
	// This lets the REST test exercise valid-ETag collision behavior without
	// weakening the generation-bound contract.
	actualExpected := expected
	actualExpected.ETag = actual.ETag
	rc, info, err := bound.GetGenerationBound(ctx, key, actualExpected)
	if err == nil {
		info.ETag = s.etag
	}
	return rc, info, err
}

func newThumbnailIdentityREST(t *testing.T, store storage.Storage, withCache bool) (*httptest.Server, repository.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "thumb.db"))
	if err != nil {
		t.Fatalf("repository.Open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		_ = repo.Close()
		t.Fatalf("repo.Migrate: %v", err)
	}
	svc := service.NewFileService(store, repo, nil).WithDeleteFailOpen(true)
	h := NewHandler(svc, nil)
	if withCache {
		h.WithThumbnailCache(thumbnail.NewCache(1<<20, 0))
	}
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv, repo
}

func TestThumbnailCacheSameTenantValidETagDifferentKeys(t *testing.T) {
	dir := t.TempDir()
	real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	counting := &countingStore{Storage: real}
	store := &forcedThumbnailETagStore{Storage: counting, etag: forcedThumbnailETag}
	srv, _ := newThumbnailIdentityREST(t, store, true)
	hdr := map[string]string{"Content-Type": "image/png"}
	put := func(key string, body []byte) {
		t.Helper()
		resp, _ := req(t, http.MethodPut, srv.URL+"/v1/files/"+key, body, hdr)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s: status=%d", key, resp.StatusCode)
		}
	}
	get := func(key string) []byte {
		t.Helper()
		resp, body := req(t, http.MethodGet, srv.URL+"/v1/files/"+key+"/thumbnail?w=32&h=32", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status=%d body=%q", key, resp.StatusCode, body)
		}
		return body
	}
	put("collision-a.png", pngBytes(t, 64, 64))
	put("collision-b.png", pngBytesAlt(t, 64, 64))
	a1 := get("collision-a.png")
	a2 := get("collision-a.png")
	b1 := get("collision-b.png")
	b2 := get("collision-b.png")
	if bytes.Equal(a1, b1) {
		t.Fatal("same-tenant valid-ETag collision served object A's thumbnail for B")
	}
	if !bytes.Equal(a1, a2) || !bytes.Equal(b1, b2) {
		t.Fatal("repeat requests must return byte-identical cached thumbnails")
	}
	if got := counting.gets.Load(); got != 2 {
		t.Fatalf("storage opens=%d, want one miss for each object", got)
	}
}

func TestThumbnailCacheEqualETagReplacementUsesVersionID(t *testing.T) {
	dir := t.TempDir()
	real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	counting := &countingStore{Storage: real}
	store := &forcedThumbnailETagStore{Storage: counting, etag: forcedThumbnailETag}
	srv, repo := newThumbnailIdentityREST(t, store, true)
	base := srv.URL + "/v1/files/replaced.png"
	hdr := map[string]string{"Content-Type": "image/png"}
	resp, body := req(t, http.MethodPut, base, pngBytes(t, 64, 64), hdr)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT A: status=%d body=%q", resp.StatusCode, body)
	}
	first, firstBody := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, nil)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("thumbnail A: status=%d", first.StatusCode)
	}
	oldValidator := first.Header.Get("ETag")
	old, err := repo.GetObject(context.Background(), "default", "default", "replaced.png")
	if err != nil {
		t.Fatalf("read A: %v", err)
	}

	resp, body = req(t, http.MethodPut, base, pngBytesAlt(t, 64, 64), hdr)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT B: status=%d body=%q", resp.StatusCode, body)
	}
	current, err := repo.GetObject(context.Background(), "default", "default", "replaced.png")
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if current.VersionID == old.VersionID {
		t.Fatal("unversioned replacement reused the authoritative VersionID")
	}
	if current.ETag != old.ETag || current.ETag != forcedThumbnailETag {
		t.Fatalf("replacement ETags differ: old=%q current=%q", old.ETag, current.ETag)
	}

	second, secondBody := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, nil)
	if second.StatusCode != http.StatusOK || bytes.Equal(firstBody, secondBody) {
		t.Fatalf("replacement response status=%d served stale thumbnail=%v", second.StatusCode, bytes.Equal(firstBody, secondBody))
	}
	if second.Header.Get("ETag") == oldValidator {
		t.Fatal("equal-ETag replacement reused the old derived validator")
	}
	beforeConditional := counting.gets.Load()
	stale, staleBody := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, map[string]string{"If-None-Match": oldValidator})
	if stale.StatusCode != http.StatusOK || !bytes.Equal(staleBody, secondBody) {
		t.Fatalf("old validator after replacement: status=%d body_changed=%v", stale.StatusCode, !bytes.Equal(staleBody, secondBody))
	}
	if counting.gets.Load() != beforeConditional {
		t.Fatal("old-validator request should use the replacement cache entry, not open stale bytes")
	}
	fresh, freshBody := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, map[string]string{"If-None-Match": second.Header.Get("ETag")})
	if fresh.StatusCode != http.StatusNotModified || len(freshBody) != 0 {
		t.Fatalf("replacement validator: status=%d body=%d, want 304/0", fresh.StatusCode, len(freshBody))
	}
	if counting.gets.Load() != beforeConditional {
		t.Fatal("matching replacement validator must not open storage")
	}
}

func TestThumbnailUnsupportedBackendOmitsUnprovenValidator(t *testing.T) {
	dir := t.TempDir()
	real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	counting := &countingStore{Storage: real}
	srv, _ := newThumbnailIdentityREST(t, &unsupportedThumbnailStore{Storage: counting}, true)
	base := srv.URL + "/v1/files/uncached.png"
	if resp, _ := req(t, http.MethodPut, base, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	thumb := base + "/thumbnail?w=32&h=32"
	first, body := req(t, http.MethodGet, thumb, nil, nil)
	if first.StatusCode != http.StatusOK || len(body) == 0 || first.Header.Get("ETag") != "" {
		t.Fatalf("unbound thumbnail: status=%d bytes=%d etag=%q", first.StatusCode, len(body), first.Header.Get("ETag"))
	}
	if first.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unbound Cache-Control=%q, want no-store", first.Header.Get("Cache-Control"))
	}
	lastModified := first.Header.Get("Last-Modified")
	if lastModified == "" {
		t.Fatal("unbound response must retain Last-Modified for date validation")
	}
	before := counting.gets.Load()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		wildcard, wildcardBody := req(t, method, thumb, nil, map[string]string{"If-None-Match": "*"})
		if wildcard.StatusCode != http.StatusNotModified || len(wildcardBody) != 0 {
			t.Fatalf("unbound %s wildcard: status=%d body=%d, want 304/0", method, wildcard.StatusCode, len(wildcardBody))
		}
	}
	if counting.gets.Load() != before {
		t.Fatalf("wildcard revalidation reopened storage: gets=%d before=%d", counting.gets.Load(), before)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		date, dateBody := req(t, method, thumb, nil, map[string]string{"If-Modified-Since": lastModified})
		if date.StatusCode != http.StatusNotModified || len(dateBody) != 0 {
			t.Fatalf("unbound %s date: status=%d body=%d, want 304/0", method, date.StatusCode, len(dateBody))
		}
	}
	if counting.gets.Load() != before {
		t.Fatalf("date revalidation reopened storage: gets=%d before=%d", counting.gets.Load(), before)
	}
	second, secondBody := req(t, http.MethodGet, thumb, nil, map[string]string{"If-None-Match": `"stale"`})
	if second.StatusCode != http.StatusOK || len(secondBody) == 0 {
		t.Fatalf("unbound conditional request: status=%d body=%d", second.StatusCode, len(secondBody))
	}
	if counting.gets.Load() != before+1 {
		t.Fatalf("unbound source should reopen after every nonmatching request: gets=%d before=%d", counting.gets.Load(), before)
	}
}

func TestThumbnailGenerationProofMismatchNoStore(t *testing.T) {
	dir := t.TempDir()
	real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	counting := &countingStore{Storage: real}
	store := &generationMismatchThumbnailStore{Storage: counting}
	srv, _ := newThumbnailIdentityREST(t, store, true)
	base := srv.URL + "/v1/files/mismatch.png"
	if resp, _ := req(t, http.MethodPut, base, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	thumb := base + "/thumbnail?w=32&h=32"
	first, body := req(t, http.MethodGet, thumb, nil, nil)
	if first.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("mismatched-proof thumbnail: status=%d bytes=%d", first.StatusCode, len(body))
	}
	if first.Header.Get("ETag") != "" || first.Header.Get("X-Version-Id") != "" {
		t.Fatalf("mismatched proof exposed validators: etag=%q version=%q", first.Header.Get("ETag"), first.Header.Get("X-Version-Id"))
	}
	if first.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("mismatched-proof Cache-Control=%q, want no-store", first.Header.Get("Cache-Control"))
	}
	before := counting.gets.Load()
	second, secondBody := req(t, http.MethodGet, thumb, nil, nil)
	if second.StatusCode != http.StatusOK || len(secondBody) == 0 {
		t.Fatalf("mismatched-proof repeat: status=%d bytes=%d", second.StatusCode, len(secondBody))
	}
	if counting.gets.Load() <= before {
		t.Fatalf("mismatched-proof response reused storage/cache: gets before=%d after=%d", before, counting.gets.Load())
	}
}

func TestThumbnailOpaqueValidatorForProviderETags(t *testing.T) {
	cases := []string{`foo"bar`, `W/"weak"`, `a,b`, "provider-" + strings.Repeat("x", 96), "control\x01etag"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
			if err != nil {
				t.Fatalf("storage.NewLocal: %v", err)
			}
			counting := &countingStore{Storage: real}
			store := &forcedThumbnailETagStore{Storage: counting, etag: raw}
			srv, _ := newThumbnailIdentityREST(t, store, true)
			base := srv.URL + "/v1/files/provider.png"
			if resp, _ := req(t, http.MethodPut, base, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
				t.Fatalf("PUT: %d", resp.StatusCode)
			}
			thumb := base + "/thumbnail?w=32&h=32"
			first, body := req(t, http.MethodGet, thumb, nil, nil)
			if first.StatusCode != http.StatusOK || len(body) == 0 {
				t.Fatalf("thumbnail: status=%d bytes=%d", first.StatusCode, len(body))
			}
			etag := first.Header.Get("ETag")
			if len(etag) < 2 || etag[0] != '"' || etag[len(etag)-1] != '"' {
				t.Fatalf("ETag=%q is not one quoted opaque-tag", etag)
			}
			inner := etag[1 : len(etag)-1]
			if strings.Contains(inner, raw) || strings.ContainsAny(inner, "\"\r\n,") {
				t.Fatalf("ETag=%q reflects unsafe source ETag %q", etag, raw)
			}
			before := counting.gets.Load()
			second, secondBody := req(t, http.MethodGet, thumb, nil, map[string]string{"If-None-Match": etag})
			if second.StatusCode != http.StatusNotModified || len(secondBody) != 0 {
				t.Fatalf("opaque validator round trip: status=%d body=%d", second.StatusCode, len(secondBody))
			}
			if counting.gets.Load() != before {
				t.Fatal("matching opaque validator opened storage")
			}
		})
	}
}

func TestThumbnailPreconditionsAndVaryBeforeOpen(t *testing.T) {
	dir := t.TempDir()
	real, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	counting := &countingStore{Storage: real}
	store := &forcedThumbnailETagStore{Storage: counting, etag: forcedThumbnailETag}
	srv, _ := newThumbnailIdentityREST(t, store, true)
	base := srv.URL + "/v1/files/conditional.png"
	if resp, _ := req(t, http.MethodPut, base, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	warm, _ := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, nil)
	validator := warm.Header.Get("ETag")
	if warm.StatusCode != http.StatusOK || validator == "" {
		t.Fatalf("warm thumbnail: status=%d etag=%q", warm.StatusCode, validator)
	}
	opens := counting.gets.Load()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		bad, body := req(t, method, base+"/thumbnail?w=32&h=32", nil, map[string]string{"If-Match": `W/` + validator})
		if bad.StatusCode != http.StatusPreconditionFailed || (method == http.MethodHead && len(body) != 0) {
			t.Fatalf("%s weak If-Match: status=%d body=%d, want 412", method, bad.StatusCode, len(body))
		}
		if counting.gets.Load() != opens {
			t.Fatalf("%s failed precondition opened storage", method)
		}
	}
	past := "Wed, 21 Oct 2015 07:28:00 GMT"
	bad, _ := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, map[string]string{"If-Unmodified-Since": past})
	if bad.StatusCode != http.StatusPreconditionFailed || counting.gets.Load() != opens {
		t.Fatalf("stale If-Unmodified-Since: status=%d opens=%d", bad.StatusCode, counting.gets.Load())
	}
	bad, _ = req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, map[string]string{
		"If-Match": `"wrong"`, "If-None-Match": validator,
	})
	if bad.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match must take precedence over matching If-None-Match: status=%d", bad.StatusCode)
	}
	if counting.gets.Load() != opens {
		t.Fatal("412 precedence path opened storage")
	}
	multi, _ := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, map[string]string{
		"If-Match": `"wrong", ` + validator,
	})
	if multi.StatusCode != http.StatusOK {
		t.Fatalf("comma-separated strong If-Match status=%d, want 200", multi.StatusCode)
	}
	weak, _ := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, map[string]string{"If-None-Match": `W/` + validator})
	if weak.StatusCode != http.StatusNotModified {
		t.Fatalf("weak If-None-Match status=%d, want 304", weak.StatusCode)
	}
	comma, _ := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, map[string]string{"If-None-Match": `"a,b", ` + validator})
	if comma.StatusCode != http.StatusNotModified {
		t.Fatalf("quoted-comma If-None-Match status=%d, want 304", comma.StatusCode)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response, body := req(t, method, base+"/thumbnail?w=32&h=32", nil, nil)
		if response.StatusCode != http.StatusOK || (method == http.MethodHead && len(body) != 0) {
			t.Fatalf("%s response: status=%d body=%d", method, response.StatusCode, len(body))
		}
		for _, token := range []string{"authorization", "x-aero-tenant", "x-api-key"} {
			if !varyContains(response.Header.Values("Vary"), token) {
				t.Fatalf("%s Vary=%q missing %s", method, response.Header.Values("Vary"), token)
			}
		}
	}
	conditional, _ := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil, map[string]string{"If-None-Match": validator})
	if conditional.StatusCode != http.StatusNotModified || !varyContains(conditional.Header.Values("Vary"), "x-aero-tenant") {
		t.Fatalf("304 Vary/status: status=%d vary=%q", conditional.StatusCode, conditional.Header.Values("Vary"))
	}
}

func varyContains(values []string, want string) bool {
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), want) {
				return true
			}
		}
	}
	return false
}

type thumbnailPreconditionSwapRepo struct {
	repository.Repository
	target  string
	initial repository.Object
	calls   atomic.Int64
}

func (r *thumbnailPreconditionSwapRepo) GetObject(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	if key == r.target && r.calls.Add(1) == 1 {
		return r.initial, nil
	}
	return r.Repository.GetObject(ctx, tenant, bucket, key)
}

func TestThumbnailStrongPreconditionRechecksOpenedGeneration(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "thumb.db"))
	if err != nil {
		t.Fatalf("repository.Open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		_ = repo.Close()
		t.Fatalf("repo.Migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		_ = repo.Close()
		t.Fatalf("storage.NewLocal: %v", err)
	}
	wrapped := &thumbnailPreconditionSwapRepo{Repository: repo, target: "race.png"}
	svc := service.NewFileService(store, wrapped, nil).WithDeleteFailOpen(true)
	h := NewHandler(svc, nil).WithThumbnailCache(thumbnail.NewCache(1<<20, 0))
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	base := srv.URL + "/v1/files/race.png"
	hdr := map[string]string{"Content-Type": "image/png"}
	if resp, _ := req(t, http.MethodPut, base, pngBytes(t, 64, 64), hdr); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT A: %d", resp.StatusCode)
	}
	first, err := repo.GetObject(context.Background(), "default", "default", "race.png")
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	if resp, _ := req(t, http.MethodPut, base, pngBytesAlt(t, 64, 64), hdr); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT B: %d", resp.StatusCode)
	}
	width, height := thumbnail.EffectiveDims(32, 32)
	oldValidator := quotedThumbETag(thumbValidatorETag(thumbnail.CacheKeyVersion,
		thumbnailSourceIdentity(first), first.ETag, width, height))
	wrapped.initial = first
	wrapped.calls.Store(0)

	resp, body := req(t, http.MethodGet, base+"/thumbnail?w=32&h=32", nil,
		map[string]string{"If-Match": oldValidator})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("raced If-Match status=%d body=%q, want 412", resp.StatusCode, body)
	}
	if h.thumbnailCache.Len() != 0 {
		t.Fatalf("failed opened-generation precondition populated cache: len=%d", h.thumbnailCache.Len())
	}
}
