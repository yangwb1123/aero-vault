package rest

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func gzipThumbnailSource(t *testing.T, source []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(source); err != nil {
		t.Fatalf("gzip source: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip source: %v", err)
	}
	return out.Bytes()
}

func newContentEncodingHarness(t *testing.T, cacheEnabled bool, admission *thumbnail.DecodeAdmission) (*httptest.Server, *countingStore, *service.FileService, *thumbnail.Cache) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "encoding.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate repo: %v", err)
	}
	realStore, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	store := &countingStore{Storage: realStore}
	svc := service.NewFileService(store, repo, nil).WithDeleteFailOpen(true)
	var cache *thumbnail.Cache
	if cacheEnabled {
		cache = thumbnail.NewCache(1<<20, 0)
	}
	h := NewHandler(svc, nil).WithThumbnailCache(cache).WithThumbnailDecodeAdmission(admission)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv, store, svc, cache
}

func TestThumbnailContentEncoding(t *testing.T) {
	srv, store, svc, _ := newThumbnailCacheREST(t, true, 0)
	sink := &recordingSink{}
	svc.WithEventSink(sink)
	source := gzipThumbnailSource(t, jpegBytesForThumbnailTest(t, 32, 16))
	for i, tc := range []struct {
		contentType string
		encoding    string
	}{
		{"image/jpeg", "gzip"},
		{"application/octet-stream", "gzip"},
		{"image/jpeg", "identity"},
		{"image/jpeg", "gzip, br"},
		{"text/plain", "gzip"},
		{"image/webp", "gzip"},
	} {
		pre := snapshotThumbCounters(t)
		pre304 := thumbnail304Counter(t)
		u := srv.URL + "/v1/files/encoded-" + string(rune('a'+i))
		resp, _ := req(t, http.MethodPut, u, source, map[string]string{
			"Content-Type": tc.contentType, "Content-Encoding": tc.encoding,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s: status=%d", tc.encoding, resp.StatusCode)
		}
		resp, body := req(t, http.MethodGet, u+"/thumbnail?w=invalid&h=-1", nil,
			map[string]string{"If-None-Match": "*"})
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("GET %s: status=%d want 415 (body=%s)", tc.encoding, resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte(`"code":"UnsupportedMediaType"`)) ||
			!bytes.Contains(body, []byte(tc.encoding)) {
			t.Fatalf("GET %s: body=%s must name the encoding and 415 code", tc.encoding, body)
		}
		for _, header := range []string{"ETag", "Last-Modified", "X-Version-Id", "Cache-Control", "Content-Encoding"} {
			if value := resp.Header.Get(header); value != "" {
				t.Errorf("GET %s: %s=%q must be absent", tc.encoding, header, value)
			}
		}
		if got := resp.Header.Get("Vary"); got != "Authorization, X-Aero-Tenant, X-Api-Key" {
			t.Fatalf("GET %s: Vary=%q must protect the metadata-derived rejection", tc.encoding, got)
		}
		if got := store.gets.Load(); got != 0 {
			t.Fatalf("GET %s opened storage %d times; encoded sources must stop before Get", tc.encoding, got)
		}
		assertThumbDeltas(t, pre, "content_encoding", 0)
		if got := sink.count(repository.EventAccessed); got != 0 {
			t.Fatalf("GET %s emitted %d access events", tc.encoding, got)
		}

		headPre := snapshotThumbCounters(t)
		headResp, headBody := req(t, http.MethodHead, u+"/thumbnail", nil, map[string]string{
			"Accept-Encoding": "gzip", "If-Modified-Since": "Wed, 21 Oct 2015 07:28:00 GMT",
		})
		if headResp.StatusCode != http.StatusUnsupportedMediaType || len(headBody) != 0 {
			t.Fatalf("HEAD %s: status=%d body=%d want 415 and no body", tc.encoding, headResp.StatusCode, len(headBody))
		}
		assertThumbDeltas(t, headPre, "content_encoding", 0)
		if got := thumbnail304Counter(t); got != pre304 {
			t.Fatalf("encoded %s changed thumbnail_304_total: %v -> %v", tc.encoding, pre304, got)
		}
		if got := sink.count(repository.EventAccessed); got != 0 {
			t.Fatalf("HEAD %s emitted %d access events", tc.encoding, got)
		}
	}
}

func thumbnail304Counter(t *testing.T) float64 {
	t.Helper()
	value, ok := scrapeValue(scrapeMetricsBody(t), "thumbnail_304_total")
	if !ok {
		return 0
	}
	return value
}

func TestUnsupportedThumbnailContentEncodingClassification(t *testing.T) {
	err := unsupportedThumbnailContentEncodingError("gzip")
	if !errors.Is(err, thumbnail.ErrUnsupportedContentEncoding) {
		t.Fatal("content-encoding error does not retain its sentinel")
	}
	if errors.Is(err, thumbnail.ErrUnsupportedFormat) {
		t.Fatal("content-encoding error must be distinct from unsupported format")
	}
	code, message, status := classify(err)
	if code != "UnsupportedMediaType" || status != http.StatusUnsupportedMediaType || !bytes.Contains([]byte(message), []byte("gzip")) {
		t.Fatalf("classify content encoding = (%q, %q, %d), want 415 with gzip", code, message, status)
	}
	reason, silent := thumbnailRejectionReason(err)
	if reason != "content_encoding" || silent {
		t.Fatalf("rejection reason = (%q, %v), want content_encoding/false", reason, silent)
	}
	long := unsupportedThumbnailContentEncodingError(string(bytes.Repeat([]byte{'x'}, maxThumbnailEncodingMessageBytes+1)))
	if bytes.Contains([]byte(long.Error()), bytes.Repeat([]byte{'x'}, maxThumbnailEncodingMessageBytes+1)) {
		t.Fatal("content-encoding error message is not bounded")
	}
}

func TestThumbnailContentEncodingLegacyWhitespace(t *testing.T) {
	srv, repo := newRESTTestWithRepo(t)
	u := srv.URL + "/v1/files/legacy.jpg"
	jpeg := jpegBytesForThumbnailTest(t, 32, 16)
	if resp, _ := req(t, http.MethodPut, u, jpeg, map[string]string{"Content-Type": "image/jpeg"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d", resp.StatusCode)
	}
	if err := repo.SetObjectMetaKey(context.Background(), "default", "default", "legacy.jpg", "_aero_content_encoding", " "); err != nil {
		t.Fatalf("set legacy metadata: %v", err)
	}
	resp, body := req(t, http.MethodGet, u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType || !bytes.Contains(body, []byte(`"code":"UnsupportedMediaType"`)) {
		t.Fatalf("whitespace metadata: status=%d body=%s", resp.StatusCode, body)
	}
}

func TestThumbnailContentEncodingAuthorizationPrecedesGate(t *testing.T) {
	srv, token, _ := newThumbnailAccessHarness(t)
	u := srv.URL + "/v1/files/private.jpg"
	source := gzipThumbnailSource(t, jpegBytesForThumbnailTest(t, 16, 16))
	if resp, _ := req(t, http.MethodPut, u, source, map[string]string{
		"Content-Type": "image/jpeg", "Content-Encoding": "gzip", "Authorization": token,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT private source: status=%d", resp.StatusCode)
	}
	pre := snapshotThumbCounters(t)
	resp, body := req(t, http.MethodGet, u+"/thumbnail?w=invalid", nil, nil)
	if resp.StatusCode != http.StatusForbidden || bytes.Contains(body, []byte("gzip")) {
		t.Fatalf("anonymous encoded source: status=%d body=%s, want opaque 403", resp.StatusCode, body)
	}
	assertThumbDeltas(t, pre, "", 0)

	policyURL := srv.URL + "/v1/buckets/default/policy"
	deny := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/policy.jpg"}]}`
	policySource := srv.URL + "/v1/files/policy.jpg"
	if resp, _ := req(t, http.MethodPut, policySource, source, map[string]string{
		"Content-Type": "image/jpeg", "Content-Encoding": "gzip", "Authorization": token,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT policy source: status=%d", resp.StatusCode)
	}
	if resp, body := req(t, http.MethodPut, policyURL, bodyPolicy(deny), map[string]string{"Authorization": "Bearer operator"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("set deny policy: status=%d body=%s", resp.StatusCode, body)
	}
	pre = snapshotThumbCounters(t)
	resp, body = req(t, http.MethodGet, policySource+"/thumbnail?w=invalid", nil, map[string]string{"Authorization": token})
	if resp.StatusCode != http.StatusForbidden || bytes.Contains(body, []byte("gzip")) {
		t.Fatalf("policy-denied encoded source: status=%d body=%s, want opaque 403", resp.StatusCode, body)
	}
	assertThumbDeltas(t, pre, "", 0)
}

func TestThumbnailContentEncodingStopsCacheAndAdmission(t *testing.T) {
	t.Run("warm cache", func(t *testing.T) {
		srv, store, svc, cache := newContentEncodingHarness(t, true, nil)
		sink := &recordingSink{}
		svc.WithEventSink(sink)
		base := srv.URL + "/v1/files/warm.jpg"
		if resp, _ := req(t, http.MethodPut, base, jpegBytesForThumbnailTest(t, 32, 16), map[string]string{"Content-Type": "image/jpeg"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: status=%d", resp.StatusCode)
		}
		thumb := base + "/thumbnail?w=16&h=16"
		if resp, _ := req(t, http.MethodGet, thumb, nil, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("warm thumbnail: status=%d", resp.StatusCode)
		}
		hits, misses, evictions, expired := cache.Stats()
		length, bytesStored := cache.Len(), cache.Bytes()
		accessed := sink.count(repository.EventAccessed)
		if err := svc.Repo().SetObjectMetaKey(context.Background(), "default", "default", "warm.jpg", "_aero_content_encoding", "gzip"); err != nil {
			t.Fatalf("set encoding metadata: %v", err)
		}
		pre := snapshotThumbCounters(t)
		pre304 := thumbnail304Counter(t)
		resp, body := req(t, http.MethodGet, thumb, nil, nil)
		if resp.StatusCode != http.StatusUnsupportedMediaType || !bytes.Contains(body, []byte("gzip")) {
			t.Fatalf("encoded warm-cache source: status=%d body=%s", resp.StatusCode, body)
		}
		assertThumbDeltas(t, pre, "content_encoding", 0)
		if got := thumbnail304Counter(t); got != pre304 {
			t.Fatalf("warm-cache rejection changed 304 counter: %v -> %v", pre304, got)
		}
		gotHits, gotMisses, gotEvictions, gotExpired := cache.Stats()
		if gotHits != hits || gotMisses != misses || gotEvictions != evictions || gotExpired != expired || cache.Len() != length || cache.Bytes() != bytesStored {
			t.Fatalf("encoded rejection touched cache: before=(%d,%d,%d,%d,%d,%d) after=(%d,%d,%d,%d,%d,%d)",
				hits, misses, evictions, expired, length, bytesStored, gotHits, gotMisses, gotEvictions, gotExpired, cache.Len(), cache.Bytes())
		}
		if got := store.gets.Load(); got != 1 {
			t.Fatalf("encoded rejection opened storage: gets=%d want 1 warm open", got)
		}
		if got := sink.count(repository.EventAccessed); got != accessed {
			t.Fatalf("encoded rejection emitted access event: before=%d after=%d", accessed, got)
		}
	})

	t.Run("held admission", func(t *testing.T) {
		admission := thumbnail.NewDecodeAdmission(1)
		release, err := admission.Acquire(context.Background(), "default")
		if err != nil {
			t.Fatalf("hold admission: %v", err)
		}
		defer release()
		srv, store, svc, _ := newContentEncodingHarness(t, true, admission)
		sink := &recordingSink{}
		svc.WithEventSink(sink)
		base := srv.URL + "/v1/files/held.jpg"
		source := gzipThumbnailSource(t, jpegBytesForThumbnailTest(t, 16, 16))
		if resp, _ := req(t, http.MethodPut, base, source, map[string]string{
			"Content-Type": "image/jpeg", "Content-Encoding": "gzip",
		}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: status=%d", resp.StatusCode)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/thumbnail?w=16&h=16", nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		done := make(chan int, 1)
		go func() {
			resp, err := http.DefaultClient.Do(rq)
			if err != nil {
				done <- 0
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			done <- resp.StatusCode
		}()
		select {
		case status := <-done:
			if status != http.StatusUnsupportedMediaType {
				t.Fatalf("held-admission encoded request status=%d want 415", status)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("encoded request blocked on decode admission")
		}
		if got := store.gets.Load(); got != 0 {
			t.Fatalf("held-admission rejection opened storage: gets=%d", got)
		}
		if got := sink.count(repository.EventAccessed); got != 0 {
			t.Fatalf("held-admission rejection emitted %d access events", got)
		}
	})
}

func TestThumbnailContentEncodingPinnedMetadataReverseOrder(t *testing.T) {
	srv, repo := newRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	u := srv.URL + "/v1/files/reverse-pinned.jpg"
	jpeg := jpegBytesForThumbnailTest(t, 32, 16)
	encoded := gzipThumbnailSource(t, jpeg)
	if resp, _ := req(t, http.MethodPut, u, encoded, map[string]string{
		"Content-Type": "image/jpeg", "Content-Encoding": "gzip",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT encoded version: status=%d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "reverse-pinned.jpg")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list encoded version: %v", err)
	}
	encodedVersion := versions[0].VersionID
	if resp, _ := req(t, http.MethodPut, u, jpeg, map[string]string{"Content-Type": "image/jpeg"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT unencoded version: status=%d", resp.StatusCode)
	}
	versions, err = repo.ListObjectVersions(context.Background(), "default", "default", "reverse-pinned.jpg")
	if err != nil || len(versions) < 2 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	unencodedVersion := versions[0].VersionID

	resp, body := req(t, http.MethodGet, u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("current unencoded version: status=%d body=%s", resp.StatusCode, body)
	}
	if _, format, err := image.Decode(bytes.NewReader(body)); err != nil || format != "jpeg" {
		t.Fatalf("current unencoded body: format=%q err=%v", format, err)
	}
	resp, body = req(t, http.MethodGet, u+"/thumbnail?version="+encodedVersion, nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType || !bytes.Contains(body, []byte("gzip")) {
		t.Fatalf("pinned encoded version: status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = req(t, http.MethodGet, u+"/thumbnail?version="+unencodedVersion, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pinned unencoded version: status=%d body=%s", resp.StatusCode, body)
	}
	if _, format, err := image.Decode(bytes.NewReader(body)); err != nil || format != "jpeg" {
		t.Fatalf("pinned unencoded body: format=%q err=%v", format, err)
	}
}

func TestThumbnailContentEncodingPinnedMetadata(t *testing.T) {
	srv, repo := newRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	u := srv.URL + "/v1/files/pinned.jpg"
	jpeg := jpegBytesForThumbnailTest(t, 32, 16)
	if resp, _ := req(t, http.MethodPut, u, jpeg, map[string]string{"Content-Type": "image/jpeg"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT unencoded version: status=%d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "pinned.jpg")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list first version: %v", err)
	}
	unencodedVersion := versions[0].VersionID
	encoded := gzipThumbnailSource(t, jpeg)
	if resp, _ := req(t, http.MethodPut, u, encoded, map[string]string{
		"Content-Type": "image/jpeg", "Content-Encoding": "gzip",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT encoded version: status=%d", resp.StatusCode)
	}
	versions, err = repo.ListObjectVersions(context.Background(), "default", "default", "pinned.jpg")
	if err != nil || len(versions) < 2 {
		t.Fatalf("list second version: %v (n=%d)", err, len(versions))
	}
	encodedVersion := versions[0].VersionID

	resp, body := req(t, http.MethodGet, u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType || !bytes.Contains(body, []byte("gzip")) {
		t.Fatalf("current encoded version: status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = req(t, http.MethodGet, u+"/thumbnail?version="+unencodedVersion, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pinned unencoded version: status=%d body=%s", resp.StatusCode, body)
	}
	if _, format, err := image.Decode(bytes.NewReader(body)); err != nil || format != "jpeg" {
		t.Fatalf("pinned unencoded body: format=%q err=%v", format, err)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("pinned unencoded derived response Content-Encoding=%q", got)
	}
	resp, body = req(t, http.MethodGet, u+"/thumbnail?version="+encodedVersion, nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType || !bytes.Contains(body, []byte("gzip")) {
		t.Fatalf("pinned encoded version: status=%d body=%s", resp.StatusCode, body)
	}
}
