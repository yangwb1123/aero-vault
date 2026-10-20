package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// Return the real older row once, then the real current row. This models a
// versioned PUT becoming visible between the authorized Stats, without sleeps
// or concurrent fixture mutation. The storage bytes and generations are real.
type thumbnailEncodingRevalidationFixture struct {
	repo    *openedContentRepo
	store   *countingStore
	svc     *service.FileService
	web     *httptest.Server
	older   repository.Object
	current repository.Object
}

func newThumbnailEncodingRevalidationFixture(t *testing.T, encoding string) *thumbnailEncodingRevalidationFixture {
	t.Helper()
	_, base, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	repo := &openedContentRepo{Repository: base}
	store := &countingStore{Storage: local}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{}).WithTenantStatusEnforcement()
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	older := putContentSelectionFixture(t, svc, "revalidate.png", pngBytes(t, 32, 16), "image/png")
	body := pngBytesAlt(t, 32, 16)
	if encoding != "" {
		body = gzipThumbnailSource(t, body)
	}
	current, err := svc.Put(context.Background(), "", "", older.Key, bytes.NewReader(body), int64(len(body)),
		service.PutOptions{ContentType: "image/png", ContentEncoding: encoding})
	if err != nil {
		t.Fatal(err)
	}
	repo.stale = older
	return &thumbnailEncodingRevalidationFixture{repo: repo, store: store, svc: svc,
		web: serveLegacyDenialFixture(t, svc, repo), older: older, current: current}
}

func (f *thumbnailEncodingRevalidationFixture) paths() []string {
	base := f.web.URL + "/v1/files/" + f.current.Key
	return []string{base + "?representation=thumbnail&w=16&h=16", base + "/thumbnail?w=16&h=16"}
}

func thumbnailEncodingValidator(obj repository.Object) string {
	width, height := thumbnail.EffectiveDims(16, 16)
	return quotedThumbETag(thumbValidatorETag(thumbnail.CacheKeyVersion,
		thumbnailSourceIdentity(obj), obj.ETag, width, height))
}

func (f *thumbnailEncodingRevalidationFixture) conditions() []map[string]string {
	return []map[string]string{
		{"If-None-Match": "*"},
		{"If-Modified-Since": f.current.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)},
		{"If-None-Match": thumbnailEncodingValidator(f.older) + ", " + thumbnailEncodingValidator(f.current)},
	}
}

func (f *thumbnailEncodingRevalidationFixture) request(t *testing.T, method, endpoint string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	f.repo.calls.Store(0)
	f.repo.armed.Store(true)
	return contentHeaderRequest(t, method, endpoint, headers)
}

func TestThumbnailEncodingRevalidationRejectsFreshEncodedSource(t *testing.T) {
	fixture := newThumbnailEncodingRevalidationFixture(t, "gzip")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range fixture.paths() {
			for _, headers := range fixture.conditions() {
				before := snapshotThumbCounters(t)
				before304 := thumbnail304Counter(t)
				response, body := fixture.request(t, method, endpoint, headers)
				assertThumbnailEncodingRevalidationRejected(t, response, body, method)
				if fixture.repo.calls.Load() != 2 || fixture.store.gets.Load() != 0 {
					t.Errorf("revalidation changed observation/open boundary: stats=%d opens=%d", fixture.repo.calls.Load(), fixture.store.gets.Load())
				}
				assertThumbDeltas(t, before, "content_encoding", 0)
				if thumbnail304Counter(t) != before304 {
					t.Error("encoded revalidation counted a successful 304")
				}
			}
		}
	}
}

func assertThumbnailEncodingRevalidationRejected(t *testing.T, response *http.Response, body []byte, method string) {
	t.Helper()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("fresh encoded source certified: method=%s url=%s status=%d headers=%v body=%q", method, response.Request.URL, response.StatusCode, response.Header, body)
	}
	assertThumbnailEncodingRevalidationBody(t, method, body)
	for _, name := range []string{"ETag", "Last-Modified", "X-Version-Id", "Cache-Control", "Content-Encoding", "Content-Range"} {
		if value := response.Header.Get(name); value != "" {
			t.Errorf("encoded rejection certified %s=%q", name, value)
		}
	}
	for _, name := range []string{"Authorization", "X-Aero-Tenant", "X-Api-Key"} {
		if !strings.Contains(response.Header.Get("Vary"), name) {
			t.Errorf("encoded rejection lost Vary %s", name)
		}
	}
}

func assertThumbnailEncodingRevalidationBody(t *testing.T, method string, body []byte) {
	t.Helper()
	if method == http.MethodGet && (!bytes.Contains(body, []byte(`"code":"UnsupportedMediaType"`)) || !bytes.Contains(body, []byte("gzip"))) {
		t.Errorf("encoded rejection lost classification: %q", body)
	}
	if method == http.MethodHead && len(body) != 0 {
		t.Errorf("HEAD rejection returned a body: %q", body)
	}
}

func TestThumbnailEncodingRevalidationKeepsFreshUnencoded304(t *testing.T) {
	fixture := newThumbnailEncodingRevalidationFixture(t, "")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range fixture.paths() {
			for _, headers := range fixture.conditions() {
				response, body := fixture.request(t, method, endpoint, headers)
				if response.StatusCode != http.StatusNotModified || len(body) != 0 || fixture.store.gets.Load() != 0 {
					t.Fatalf("healthy revalidation changed: status=%d opens=%d body=%q", response.StatusCode, fixture.store.gets.Load(), body)
				}
				if response.Header.Get("ETag") != thumbnailEncodingValidator(fixture.current) || response.Header.Get("X-Version-Id") != fixture.current.VersionID {
					t.Errorf("healthy 304 lost fresh identity: %v", response.Header)
				}
			}
		}
	}
}

func TestThumbnailEncodingRevalidationAuthorizationFailurePrecedesEncoding(t *testing.T) {
	for _, failure := range []error{nil, errors.New("revalidation authorization unavailable")} {
		fixture := newThumbnailEncodingRevalidationFixture(t, "gzip")
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range fixture.paths() {
				provider := &readBoundaryProvider{rejectAt: 2, failure: failure}
				fixture.svc.WithAuthorizer(provider)
				response, body := fixture.request(t, method, endpoint, map[string]string{"If-None-Match": "*"})
				want := http.StatusForbidden
				if failure != nil {
					want = http.StatusInternalServerError
				}
				if response.StatusCode != want || provider.reads.Load() != 2 || fixture.store.gets.Load() != 0 || bytes.Contains(body, []byte("gzip")) {
					t.Errorf("revalidation authorization bypassed/leaked: status=%d checks=%d opens=%d body=%q", response.StatusCode, provider.reads.Load(), fixture.store.gets.Load(), body)
				}
			}
		}
	}
}

func TestThumbnailEncodingRevalidationHistoricalPinIgnoresEncodedCurrent(t *testing.T) {
	fixture := newThumbnailEncodingRevalidationFixture(t, "gzip")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range fixture.paths() {
			endpoint += "&version=" + fixture.older.VersionID
			response, body := fixture.request(t, method, endpoint, map[string]string{"If-None-Match": "*"})
			if response.StatusCode != http.StatusNotModified || len(body) != 0 || fixture.store.gets.Load() != 0 ||
				response.Header.Get("ETag") != thumbnailEncodingValidator(fixture.older) || response.Header.Get("X-Version-Id") != fixture.older.VersionID {
				t.Errorf("historical revalidation consulted current encoding: status=%d opens=%d headers=%v body=%q", response.StatusCode, fixture.store.gets.Load(), response.Header, body)
			}
		}
	}
}

func TestThumbnailEncodingRevalidationOpenAPIDocumentsRejection(t *testing.T) {
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
		for _, phrase := range []string{"Content-Encoding", "second authorized conditional Stat", "415, not 304"} {
			if !strings.Contains(description, phrase) {
				t.Errorf("%s %s missing 415 contract %q: %q", check.method, check.path, phrase, description)
			}
		}
	}
}

func TestThumbnailEncodingRevalidationDoesNotRejectRawEncodedContent(t *testing.T) {
	fixture := newThumbnailEncodingRevalidationFixture(t, "gzip")
	expected := gzipThumbnailSource(t, pngBytesAlt(t, 32, 16))
	for _, endpoint := range readBoundaryContentPaths(fixture.web.URL+"/v1", fixture.current.Key, "") {
		response, body := contentHeaderRequest(t, http.MethodGet, endpoint, nil)
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(body, expected) {
			t.Errorf("raw encoded content changed: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
		}
	}
}
