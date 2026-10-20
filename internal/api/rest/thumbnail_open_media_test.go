package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/service"
)

// Reuse real local generations, version rows, authenticated HTTP, and the
// deterministic old-Stat/new-open seam. Only the new row's declaration/bytes
// differ; no synthetic MIME metadata or generation proof is supplied.
func newThumbnailMediaOpenFixture(t *testing.T, staleType, currentType string, body []byte, bound bool) *thumbnailEncodingOpenFixture {
	t.Helper()
	f := newThumbnailEncodingOpenFixture(t, staleType, "", bound)
	current, err := f.svc.Put(context.Background(), "", "", f.current.Key, bytes.NewReader(body), int64(len(body)),
		service.PutOptions{ContentType: currentType})
	if err != nil {
		t.Fatal(err)
	}
	f.current = current
	return f
}

type thumbnailMediaRejection struct {
	contentType  string
	status       int
	code, reason string
	reads        bool
}

func checkThumbnailMediaOpenRejection(t *testing.T, f *thumbnailEncodingOpenFixture, method, endpoint string, want thumbnailMediaRejection) {
	t.Helper()
	before := snapshotThumbCounters(t)
	before304 := thumbnail304Counter(t)
	response, body := f.request(t, method, endpoint)
	if response.StatusCode != want.status {
		t.Errorf("opened Content-Type ignored: type=%q method=%s url=%s status=%d want=%d headers=%v body=%q",
			want.contentType, method, endpoint, response.StatusCode, want.status, response.Header, body)
	}
	assertThumbnailMediaOpenErrorBody(t, body, method, want.code)
	assertThumbnailMediaOpenErrorHeaders(t, response)
	assertThumbnailEncodingOpenSource(t, f, f.current, want.reads)
	assertThumbDeltas(t, before, want.reason, 0)
	if f.repo.calls.Load() != 2 || f.cache.Len() != 0 || f.cache.Bytes() != 0 || thumbnail304Counter(t) != before304 {
		t.Errorf("media rejection changed observation/cache/304: stats=%d entries=%d bytes=%d", f.repo.calls.Load(), f.cache.Len(), f.cache.Bytes())
	}
}

func assertThumbnailMediaOpenErrorBody(t *testing.T, body []byte, method, code string) {
	t.Helper()
	if method == http.MethodGet && !bytes.Contains(body, []byte(`"code":"`+code+`"`)) {
		t.Errorf("opened media classification lost: %q", body)
	}
	if method == http.MethodHead && len(body) != 0 {
		t.Errorf("HEAD rejection returned body: %q", body)
	}
	for _, private := range []string{"media-close-token", "objects.example.test"} {
		if bytes.Contains(body, []byte(private)) {
			t.Errorf("media cleanup diagnostic leaked: %q", body)
		}
	}
}

func assertThumbnailMediaOpenErrorHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	for _, name := range []string{"ETag", "Last-Modified", "X-Version-Id", "Cache-Control", "Content-Encoding", "Content-Range"} {
		if response.Header.Get(name) != "" {
			t.Errorf("media rejection certified %s=%q", name, response.Header.Get(name))
		}
	}
	for _, name := range []string{"Authorization", "X-Aero-Tenant", "X-Api-Key"} {
		if !strings.Contains(response.Header.Get("Vary"), name) {
			t.Errorf("media rejection lost Vary %s", name)
		}
	}
}

func TestThumbnailOpenMediaRejectsChangedDeclarationBeforeRead(t *testing.T) {
	for _, staleType := range []string{"image/png", "application/octet-stream"} {
		for _, want := range []thumbnailMediaRejection{
			{"image/webp", http.StatusUnsupportedMediaType, "UnsupportedMediaType", "unsupported_format", false},
			{"text/plain", http.StatusBadRequest, "InvalidArgument", "invalid_argument", false},
			{"not-a-media-type", http.StatusBadRequest, "InvalidArgument", "invalid_argument", false},
		} {
			for _, bound := range []bool{true, false} {
				f := newThumbnailMediaOpenFixture(t, staleType, want.contentType, pngBytesAlt(t, 32, 16), bound)
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, endpoint := range f.paths() {
						checkThumbnailMediaOpenRejection(t, f, method, endpoint, want)
					}
				}
			}
		}
	}
}

func TestThumbnailOpenMediaUsesOpenedGenericSniffDecision(t *testing.T) {
	for _, contentType := range []string{"application/octet-stream", "", `image/png; broken="`} {
		for _, bound := range []bool{true, false} {
			f := newThumbnailMediaOpenFixture(t, "image/png", contentType, webpBytes, bound)
			want := thumbnailMediaRejection{contentType, http.StatusUnsupportedMediaType, "UnsupportedMediaType", "unsupported_format", true}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, endpoint := range f.paths() {
					checkThumbnailMediaOpenRejection(t, f, method, endpoint, want)
				}
			}
		}
	}
}

func TestThumbnailOpenMediaDoesNotReuseStaleGenericDecision(t *testing.T) {
	// Declared supported types use the decoder, not generic-MIME magic
	// admission. Preserve the existing 400 for unsupported bytes in that arm.
	for _, bound := range []bool{true, false} {
		f := newThumbnailMediaOpenFixture(t, "application/octet-stream", "Image/PNG; charset=utf-8", webpBytes, bound)
		want := thumbnailMediaRejection{"Image/PNG; charset=utf-8", http.StatusBadRequest, "InvalidArgument", "unsupported", true}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				checkThumbnailMediaOpenRejection(t, f, method, endpoint, want)
			}
		}
	}
}

func assertThumbnailMediaOpenIdentity(t *testing.T, f *thumbnailEncodingOpenFixture, response *http.Response, bound bool) {
	t.Helper()
	if bound && (response.Header.Get("ETag") != thumbnailEncodingValidator(f.current) || response.Header.Get("X-Version-Id") != f.current.VersionID) {
		t.Errorf("replacement lost opened identity: %v", response.Header)
	}
	if !bound && (response.Header.Get("ETag") != "" || response.Header.Get("X-Version-Id") != "" || response.Header.Get("Cache-Control") != "no-store") {
		t.Errorf("fallback incorrectly certified source: %v", response.Header)
	}
}

func TestThumbnailOpenMediaDecodableReplacementStillGenerates(t *testing.T) {
	for _, currentType := range []string{"Image/PNG; charset=utf-8", "application/octet-stream", "", `image/png; broken="`} {
		for _, bound := range []bool{true, false} {
			f := newThumbnailMediaOpenFixture(t, "image/png", currentType, pngBytesAlt(t, 32, 16), bound)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, endpoint := range f.paths() {
					response, body := f.request(t, method, endpoint)
					assertThumbnailEncodingOpenHealthy(t, response, body, method)
					assertThumbnailEncodingOpenSource(t, f, f.current, true)
					assertThumbnailMediaOpenIdentity(t, f, response, bound)
				}
			}
		}
	}
}

func TestThumbnailOpenMediaAuthorizationFailurePrecedesDeclaration(t *testing.T) {
	for _, failure := range []error{nil, errors.New("opening authorization unavailable")} {
		f := newThumbnailMediaOpenFixture(t, "image/png", "image/webp", pngBytesAlt(t, 32, 16), true)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				provider := &readBoundaryProvider{rejectAt: 2, failure: failure}
				f.svc.WithAuthorizer(provider)
				response, body := f.request(t, method, endpoint)
				want := http.StatusForbidden
				if failure != nil {
					want = http.StatusInternalServerError
				}
				if response.StatusCode != want || provider.reads.Load() != 2 || f.store.opens.Load() != 0 || bytes.Contains(body, []byte("webp")) {
					t.Errorf("opening auth bypassed/leaked declaration: status=%d checks=%d opens=%d body=%q", response.StatusCode, provider.reads.Load(), f.store.opens.Load(), body)
				}
			}
		}
	}
}

func TestThumbnailOpenMediaCleanupErrorDoesNotReplaceRejection(t *testing.T) {
	for _, want := range []thumbnailMediaRejection{
		{"image/webp", http.StatusUnsupportedMediaType, "UnsupportedMediaType", "unsupported_format", false},
		{"text/plain", http.StatusBadRequest, "InvalidArgument", "invalid_argument", false},
	} {
		f := newThumbnailMediaOpenFixture(t, "image/png", want.contentType, pngBytesAlt(t, 32, 16), true)
		f.store.closeErr = errors.New("provider=https://objects.example.test secret=media-close-token")
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				checkThumbnailMediaOpenRejection(t, f, method, endpoint, want)
				assertThumbnailMediaAdmissionAvailable(t, f)
			}
		}
	}
}

func assertThumbnailMediaAdmissionAvailable(t *testing.T, f *thumbnailEncodingOpenFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := f.admission.Acquire(ctx, service.DefaultTenant)
	if err != nil {
		t.Fatal("media rejection leaked decode admission:", err)
	}
	release()
}

func TestThumbnailOpenMediaHistoricalPinIgnoresCurrentDeclaration(t *testing.T) {
	for _, olderType := range []string{"image/png", "application/octet-stream"} {
		f := newThumbnailMediaOpenFixture(t, olderType, "image/webp", webpBytes, true)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				f.cache.InvalidateSource(thumbnailSourceIdentity(f.older))
				response, body := f.request(t, method, endpoint+"&version="+f.older.VersionID)
				assertThumbnailEncodingOpenHealthy(t, response, body, method)
				assertThumbnailEncodingOpenSource(t, f, f.older, true)
				if response.Header.Get("ETag") != thumbnailEncodingValidator(f.older) || response.Header.Get("X-Version-Id") != f.older.VersionID {
					t.Errorf("pin mixed current media/identity: %v", response.Header)
				}
			}
		}
	}
}

func TestThumbnailOpenMediaIndependentRequestRecovers(t *testing.T) {
	f := newThumbnailMediaOpenFixture(t, "image/png", "image/webp", pngBytesAlt(t, 32, 16), true)
	want := thumbnailMediaRejection{"image/webp", http.StatusUnsupportedMediaType, "UnsupportedMediaType", "unsupported_format", false}
	for _, endpoint := range f.paths() {
		checkThumbnailMediaOpenRejection(t, f, http.MethodGet, endpoint, want)
	}
	f.current = putContentSelectionFixture(t, f.svc, f.current.Key, pngBytesAlt(t, 32, 16), "application/octet-stream")
	for _, endpoint := range f.paths() {
		response, body := f.request(t, http.MethodGet, endpoint)
		assertThumbnailEncodingOpenHealthy(t, response, body, http.MethodGet)
		assertThumbnailEncodingOpenSource(t, f, f.current, true)
		assertThumbnailMediaOpenIdentity(t, f, response, true)
	}
}

func TestThumbnailOpenMediaPreflightRejectionRemainsTerminal(t *testing.T) {
	for _, check := range []struct {
		contentType string
		status      int
	}{{"text/plain", http.StatusBadRequest}, {"image/webp", http.StatusUnsupportedMediaType}} {
		f := newThumbnailMediaOpenFixture(t, check.contentType, "image/png", pngBytesAlt(t, 32, 16), true)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				response, _ := f.request(t, method, endpoint)
				if response.StatusCode != check.status || f.repo.calls.Load() != 1 || f.store.opens.Load() != 0 || f.store.reads.Load() != 0 || f.store.closes.Load() != 0 {
					t.Errorf("preflight rejection reopened source: status=%d stats=%d opens=%d reads=%d closes=%d",
						response.StatusCode, f.repo.calls.Load(), f.store.opens.Load(), f.store.reads.Load(), f.store.closes.Load())
				}
			}
		}
	}
}

func TestThumbnailOpenMediaOpenAPIDocumentsSelection(t *testing.T) {
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
		responses := spec.Paths[check.path][check.method].Responses
		for _, status := range []string{"400", "415"} {
			if !strings.Contains(responses[status].Description, "opened source Content-Type") {
				t.Errorf("%s %s missing opened media %s contract: %q", check.method, check.path, status, responses[status].Description)
			}
		}
	}
}
