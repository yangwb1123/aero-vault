package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Keep the optional generation capability real while observing the exact
// descriptor's reads/close. Disabling it exercises the ordinary local fallback.
type thumbnailEncodingOpenStore struct {
	*openedContentStore
	bound    bool
	closeErr error
}

func (s *thumbnailEncodingOpenStore) GenerationBoundAvailable() bool { return s.bound }

func (s *thumbnailEncodingOpenStore) GetGenerationBound(ctx context.Context, key string, expected storage.ObjectInfo) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.(storage.GenerationBoundStorage).GetGenerationBound(ctx, key, expected)
	if err != nil {
		return rc, info, err
	}
	s.key.Store(key)
	s.opens.Add(1)
	return &thumbnailEncodingOpenSource{ReadCloser: &openedContentSource{ReadCloser: rc, store: s.openedContentStore}, closeErr: s.closeErr}, info, nil
}

func (s *thumbnailEncodingOpenStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.openedContentStore.Get(ctx, key)
	if err != nil {
		return rc, info, err
	}
	return &thumbnailEncodingOpenSource{ReadCloser: rc, closeErr: s.closeErr}, info, nil
}

type thumbnailEncodingOpenSource struct {
	io.ReadCloser
	closeErr error
}

func (s *thumbnailEncodingOpenSource) Close() error {
	err := s.ReadCloser.Close()
	if s.closeErr != nil {
		return s.closeErr
	}
	return err
}

type thumbnailEncodingOpenFixture struct {
	repo      *openedContentRepo
	store     *thumbnailEncodingOpenStore
	svc       *service.FileService
	web       *httptest.Server
	cache     *thumbnail.Cache
	admission *thumbnail.DecodeAdmission
	sink      *recordingSink
	older     repository.Object
	current   repository.Object
}

func newThumbnailEncodingOpenFixture(t *testing.T, contentType, encoding string, bound bool) *thumbnailEncodingOpenFixture {
	t.Helper()
	_, base, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	repo := &openedContentRepo{Repository: base}
	store := &thumbnailEncodingOpenStore{openedContentStore: &openedContentStore{Storage: local}, bound: bound}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{}).WithTenantStatusEnforcement()
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	older := putContentSelectionFixture(t, svc, "open-encoding.png", pngBytes(t, 32, 16), contentType)
	body := pngBytesAlt(t, 32, 16)
	if encoding == "gzip" {
		body = gzipThumbnailSource(t, body)
	}
	current, err := svc.Put(context.Background(), "", "", older.Key, bytes.NewReader(body), int64(len(body)),
		service.PutOptions{ContentType: contentType, ContentEncoding: encoding})
	if err != nil {
		t.Fatal(err)
	}
	repo.stale = older
	fixture := &thumbnailEncodingOpenFixture{repo: repo, store: store, svc: svc, older: older, current: current,
		cache: thumbnail.NewCache(1<<20, 0), admission: thumbnail.NewDecodeAdmission(1), sink: &recordingSink{}}
	svc.WithEventSink(fixture.sink)
	fixture.web = serveThumbnailEncodingOpenFixture(t, fixture)
	return fixture
}

func serveThumbnailEncodingOpenFixture(t *testing.T, f *thumbnailEncodingOpenFixture) *httptest.Server {
	t.Helper()
	registry, err := auth.Parse("reader:default:read+write")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := chi.NewRouter()
	router.Mount("/v1", NewRouter(f.svc, f.repo, nil, nil, nil, nil, registry, logger, false, nil, nil, 0, false,
		func(h *Handler) { h.WithThumbnailCache(f.cache).WithThumbnailDecodeAdmission(f.admission) }))
	handler := server.ApplyMiddleware(router, f.repo, registry, mw.NewRateLimiter(0, 0), &config.Config{}, logger,
		mw.NewConcurrencyLimiter(0).Middleware(), nil)
	web := httptest.NewServer(handler)
	t.Cleanup(web.Close)
	return web
}

func (f *thumbnailEncodingOpenFixture) paths() []string {
	base := f.web.URL + "/v1/files/" + f.current.Key
	return []string{base + "?representation=thumbnail&w=16&h=16", base + "/thumbnail?w=16&h=16"}
}

func (f *thumbnailEncodingOpenFixture) request(t *testing.T, method, endpoint string) (*http.Response, []byte) {
	t.Helper()
	f.repo.calls.Store(0)
	f.repo.armed.Store(true)
	f.store.opens.Store(0)
	f.store.reads.Store(0)
	f.store.closes.Store(0)
	f.store.key.Store("")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer reader")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func assertThumbnailEncodingOpenSource(t *testing.T, f *thumbnailEncodingOpenFixture, selected repository.Object, reads bool) {
	t.Helper()
	if f.store.opens.Load() != 1 || f.store.closes.Load() != 1 || f.store.key.Load() != selected.StorageKey {
		t.Errorf("opened source reselected/leaked: opens=%d closes=%d key=%v want=%s", f.store.opens.Load(), f.store.closes.Load(), f.store.key.Load(), selected.StorageKey)
	}
	if (f.store.reads.Load() > 0) != reads {
		t.Errorf("unexpected source reads=%d; want reads=%v", f.store.reads.Load(), reads)
	}
}

func assertThumbnailEncodingOpenRejected(t *testing.T, response *http.Response, body []byte, method, encoding string) {
	t.Helper()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("opened encoding ignored: method=%s url=%s status=%d headers=%v body=%q", method, response.Request.URL, response.StatusCode, response.Header, body)
	}
	assertThumbnailEncodingOpenBody(t, body, method, encoding)
	for _, name := range []string{"ETag", "Last-Modified", "X-Version-Id", "Cache-Control", "Content-Encoding", "Content-Range"} {
		if response.Header.Get(name) != "" {
			t.Errorf("rejection certified %s=%q", name, response.Header.Get(name))
		}
	}
	for _, name := range []string{"Authorization", "X-Aero-Tenant", "X-Api-Key"} {
		if !strings.Contains(response.Header.Get("Vary"), name) {
			t.Errorf("rejection lost Vary %s", name)
		}
	}
}

func assertThumbnailEncodingOpenBody(t *testing.T, body []byte, method, encoding string) {
	t.Helper()
	if method == http.MethodGet && (!bytes.Contains(body, []byte(`"code":"UnsupportedMediaType"`)) || !bytes.Contains(body, []byte(encoding))) {
		t.Errorf("encoding classification lost: %q", body)
	}
	if method == http.MethodHead && len(body) != 0 {
		t.Errorf("HEAD returned a body: %q", body)
	}
}

func checkThumbnailEncodingOpenRejection(t *testing.T, f *thumbnailEncodingOpenFixture, method, endpoint, encoding string) {
	t.Helper()
	before := snapshotThumbCounters(t)
	before304 := thumbnail304Counter(t)
	response, body := f.request(t, method, endpoint)
	assertThumbnailEncodingOpenRejected(t, response, body, method, encoding)
	assertThumbnailEncodingOpenSource(t, f, f.current, false)
	assertThumbDeltas(t, before, "content_encoding", 0)
	if f.repo.calls.Load() != 2 || f.cache.Len() != 0 || f.cache.Bytes() != 0 || thumbnail304Counter(t) != before304 {
		t.Errorf("rejection changed cache/observation/304: stats=%d entries=%d bytes=%d", f.repo.calls.Load(), f.cache.Len(), f.cache.Bytes())
	}
}

func TestThumbnailOpenEncodingRejectsChangedSourceBeforeRead(t *testing.T) {
	for _, contentType := range []string{"image/png", "application/octet-stream"} {
		for _, encoding := range []string{"gzip", "identity"} {
			for _, bound := range []bool{true, false} {
				f := newThumbnailEncodingOpenFixture(t, contentType, encoding, bound)
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, endpoint := range f.paths() {
						checkThumbnailEncodingOpenRejection(t, f, method, endpoint, encoding)
					}
				}
				if f.sink.count(repository.EventAccessed) != 4 {
					t.Errorf("opened attempts lost existing access events: %d", f.sink.count(repository.EventAccessed))
				}
			}
		}
	}
}

func TestThumbnailOpenEncodingAuthorizationFailurePrecedesEncoding(t *testing.T) {
	for _, failure := range []error{nil, errors.New("opening authorization unavailable")} {
		f := newThumbnailEncodingOpenFixture(t, "image/png", "gzip", true)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				provider := &readBoundaryProvider{rejectAt: 2, failure: failure}
				f.svc.WithAuthorizer(provider)
				response, body := f.request(t, method, endpoint)
				want := http.StatusForbidden
				if failure != nil {
					want = http.StatusInternalServerError
				}
				if response.StatusCode != want || provider.reads.Load() != 2 || f.store.opens.Load() != 0 || bytes.Contains(body, []byte("gzip")) {
					t.Errorf("opening auth bypassed/leaked encoding: status=%d checks=%d opens=%d body=%q", response.StatusCode, provider.reads.Load(), f.store.opens.Load(), body)
				}
			}
		}
	}
}

func TestThumbnailOpenEncodingCleanupErrorDoesNotReplace415(t *testing.T) {
	f := newThumbnailEncodingOpenFixture(t, "application/octet-stream", "identity", true)
	f.store.closeErr = errors.New("provider=https://objects.example.test secret=close-token")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			response, body := f.request(t, method, endpoint)
			assertThumbnailEncodingOpenRejected(t, response, body, method, "identity")
			assertThumbnailEncodingOpenSource(t, f, f.current, false)
			if bytes.Contains(body, []byte("close-token")) || bytes.Contains(body, []byte("objects.example.test")) {
				t.Errorf("cleanup diagnostic leaked: %q", body)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			release, err := f.admission.Acquire(ctx, service.DefaultTenant)
			cancel()
			if err != nil {
				t.Fatal("rejection leaked decode admission:", err)
			}
			release()
		}
	}
}

func TestThumbnailOpenEncodingUnencodedReplacementStillGenerates(t *testing.T) {
	for _, contentType := range []string{"image/png", "application/octet-stream"} {
		for _, bound := range []bool{true, false} {
			f := newThumbnailEncodingOpenFixture(t, contentType, "", bound)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, endpoint := range f.paths() {
					response, body := f.request(t, method, endpoint)
					assertThumbnailEncodingOpenHealthy(t, response, body, method)
					assertThumbnailEncodingOpenSource(t, f, f.current, true)
					if bound && (response.Header.Get("ETag") != thumbnailEncodingValidator(f.current) || response.Header.Get("X-Version-Id") != f.current.VersionID) {
						t.Errorf("unencoded replacement lost opened identity: %v", response.Header)
					}
				}
			}
		}
	}
}

func assertThumbnailEncodingOpenHealthy(t *testing.T, response *http.Response, body []byte, method string) {
	t.Helper()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("healthy source rejected: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}
	if method == http.MethodHead {
		if len(body) != 0 {
			t.Errorf("healthy HEAD returned %d bytes", len(body))
		}
		return
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || format != "jpeg" {
		t.Fatalf("healthy thumbnail malformed: format=%q err=%v", format, err)
	}
	if img.Bounds().Dx() != 16 || img.Bounds().Dy() != 8 {
		t.Errorf("healthy thumbnail dimensions=%v", img.Bounds())
	}
}

func TestThumbnailOpenEncodingMissingBlobPrecedesGate(t *testing.T) {
	f := newThumbnailEncodingOpenFixture(t, "image/png", "gzip", true)
	if err := f.store.Storage.Delete(context.Background(), f.current.StorageKey); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			before := snapshotThumbCounters(t)
			response, body := f.request(t, method, endpoint)
			if response.StatusCode != http.StatusNotFound || f.store.opens.Load() != 0 || bytes.Contains(body, []byte("gzip")) {
				t.Errorf("open failure replaced/leaked encoding: status=%d opens=%d body=%q", response.StatusCode, f.store.opens.Load(), body)
			}
			assertThumbDeltas(t, before, "source_error", 0)
		}
	}
}

func TestThumbnailOpenEncodingIndependentRequestRecovers(t *testing.T) {
	f := newThumbnailEncodingOpenFixture(t, "image/png", "identity", true)
	for _, endpoint := range f.paths() {
		checkThumbnailEncodingOpenRejection(t, f, http.MethodGet, endpoint, "identity")
	}
	f.current = putContentSelectionFixture(t, f.svc, f.current.Key, pngBytesAlt(t, 32, 16), "image/png")
	for _, endpoint := range f.paths() {
		response, body := f.request(t, http.MethodGet, endpoint)
		assertThumbnailEncodingOpenHealthy(t, response, body, http.MethodGet)
		assertThumbnailEncodingOpenSource(t, f, f.current, true)
	}
}

func TestThumbnailOpenEncodingLegacyDeclarationBounded(t *testing.T) {
	for _, encoding := range []string{" ", strings.Repeat("x", maxThumbnailEncodingMessageBytes+1)} {
		f := newThumbnailEncodingOpenFixture(t, "image/png", "", true)
		if err := f.repo.SetObjectMetaKey(context.Background(), "default", "default", f.current.Key, "_aero_content_encoding", encoding); err != nil {
			t.Fatal(err)
		}
		response, body := f.request(t, http.MethodGet, f.paths()[0])
		assertThumbnailEncodingOpenRejected(t, response, body, http.MethodGet, boundedThumbnailEncoding(encoding))
		assertThumbnailEncodingOpenSource(t, f, f.current, false)
		if bytes.Contains(body, []byte(strings.Repeat("x", maxThumbnailEncodingMessageBytes+1))) {
			t.Errorf("opened encoding message unbounded: %q", body)
		}
	}
}

func TestThumbnailOpenEncodingOpenAPIDocumentsRejection(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct{ Description string }
		}
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ path, method string }{
		{"/v1/files/{key}", "get"}, {"/v1/files/{key}", "head"}, {"/v1/files/{key}/thumbnail", "get"},
	} {
		description := spec.Paths[check.path][check.method].Responses["415"].Description
		for _, phrase := range []string{"Content-Encoding", "authorized opened source", "before sniff/decode"} {
			if !strings.Contains(description, phrase) {
				t.Errorf("%s %s missing opened 415 contract %q: %q", check.method, check.path, phrase, description)
			}
		}
	}
}

func TestThumbnailOpenEncodingHistoricalPinIgnoresEncodedCurrent(t *testing.T) {
	f := newThumbnailEncodingOpenFixture(t, "application/octet-stream", "gzip", true)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			// Empty the derived cache so this control exercises the pinned opener.
			f.cache.InvalidateSource(thumbnailSourceIdentity(f.older))
			response, body := f.request(t, method, endpoint+"&version="+f.older.VersionID)
			assertThumbnailEncodingOpenHealthy(t, response, body, method)
			assertThumbnailEncodingOpenSource(t, f, f.older, true)
			if response.Header.Get("ETag") != thumbnailEncodingValidator(f.older) || response.Header.Get("X-Version-Id") != f.older.VersionID {
				t.Errorf("pin mixed current encoding/identity: %v", response.Header)
			}
		}
	}
}
