package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Use real version rows/Blob generations and authenticated HTTP through the
// shared middleware chain. The seam only makes the real older row visible to
// the first Stat; the second authorized Stat observes the real current row.
func thumbnailMediaRevalidationRequest(t *testing.T, f *thumbnailEncodingOpenFixture, method, endpoint string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	f.repo.calls.Store(0)
	f.repo.armed.Store(true)
	f.store.opens.Store(0)
	f.store.reads.Store(0)
	f.store.closes.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer reader")
	request.Header.Set("Accept-Encoding", "identity")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
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

func thumbnailMediaRevalidationConditions(f *thumbnailEncodingOpenFixture, bound bool) []map[string]string {
	conditions := []map[string]string{
		{"If-None-Match": "*"},
		{"If-Modified-Since": f.current.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)},
	}
	if bound {
		conditions = append(conditions, map[string]string{
			"If-None-Match": thumbnailEncodingValidator(f.older) + ", " + thumbnailEncodingValidator(f.current),
		})
	}
	return conditions
}

func assertThumbnailMediaRevalidationNoPipeline(t *testing.T, f *thumbnailEncodingOpenFixture) {
	t.Helper()
	if f.repo.calls.Load() != 2 {
		t.Errorf("revalidation changed Stat boundary: stats=%d want=2", f.repo.calls.Load())
	}
	assertThumbnailMediaRevalidationNoSource(t, f)
}

func assertThumbnailMediaRevalidationNoSource(t *testing.T, f *thumbnailEncodingOpenFixture) {
	t.Helper()
	if f.store.opens.Load() != 0 || f.store.reads.Load() != 0 || f.store.closes.Load() != 0 {
		t.Errorf("metadata-only revalidation opened/read/closed source: opens=%d reads=%d closes=%d",
			f.store.opens.Load(), f.store.reads.Load(), f.store.closes.Load())
	}
	if f.cache.Len() != 0 || f.cache.Bytes() != 0 || f.sink.count(repository.EventAccessed) != 0 {
		t.Errorf("metadata-only revalidation entered cache/access path: entries=%d bytes=%d access=%d",
			f.cache.Len(), f.cache.Bytes(), f.sink.count(repository.EventAccessed))
	}
}

func checkThumbnailMediaRevalidationRejection(t *testing.T, f *thumbnailEncodingOpenFixture, method, endpoint string, headers map[string]string, want thumbnailMediaRejection) {
	t.Helper()
	before := snapshotThumbCounters(t)
	before304 := thumbnail304Counter(t)
	response, body := thumbnailMediaRevalidationRequest(t, f, method, endpoint, headers)
	if response.StatusCode != want.status {
		t.Errorf("second Stat Content-Type ignored: type=%q method=%s url=%s condition=%v status=%d want=%d headers=%v body=%q",
			want.contentType, method, endpoint, headers, response.StatusCode, want.status, response.Header, body)
	}
	assertThumbnailMediaOpenErrorBody(t, body, method, want.code)
	assertThumbnailMediaOpenErrorHeaders(t, response)
	assertThumbnailMediaRevalidationNoPipeline(t, f)
	assertThumbDeltas(t, before, want.reason, 0)
	if thumbnail304Counter(t) != before304 {
		t.Error("media rejection counted a successful 304")
	}
}

func TestThumbnailMediaRevalidationRejectsChangedDeclaration(t *testing.T) {
	for _, want := range []thumbnailMediaRejection{
		{"image/webp", http.StatusUnsupportedMediaType, "UnsupportedMediaType", "unsupported_format", false},
		{"text/plain", http.StatusBadRequest, "InvalidArgument", "invalid_argument", false},
		{"not-a-media-type", http.StatusBadRequest, "InvalidArgument", "invalid_argument", false},
	} {
		for _, bound := range []bool{true, false} {
			f := newThumbnailMediaOpenFixture(t, "image/png", want.contentType, pngBytesAlt(t, 32, 16), bound)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, endpoint := range f.paths() {
					for _, headers := range thumbnailMediaRevalidationConditions(f, bound) {
						checkThumbnailMediaRevalidationRejection(t, f, method, endpoint, headers, want)
					}
				}
			}
		}
	}
}

func TestThumbnailMediaRevalidationKeepsFreshSupported304(t *testing.T) {
	for _, bound := range []bool{true, false} {
		f := newThumbnailMediaOpenFixture(t, "image/png", "Image/PNG; charset=utf-8", pngBytesAlt(t, 32, 16), bound)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				for _, headers := range thumbnailMediaRevalidationConditions(f, bound) {
					checkThumbnailMediaRevalidation304(t, f, method, endpoint, headers, f.current, bound)
					assertThumbnailMediaRevalidationNoPipeline(t, f)
				}
			}
		}
	}
}

func assertThumbnailMediaRevalidationHealthy304(t *testing.T, f *thumbnailEncodingOpenFixture, response *http.Response, body []byte, selected repository.Object, bound bool) {
	t.Helper()
	assertThumbnailMediaRevalidationNoSource(t, f)
	if response.StatusCode != http.StatusNotModified || len(body) != 0 || response.Header.Get("Last-Modified") != selected.UpdatedAt.UTC().Format(http.TimeFormat) {
		t.Errorf("healthy revalidation changed: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}
	if bound {
		if response.Header.Get("ETag") != thumbnailEncodingValidator(selected) || response.Header.Get("X-Version-Id") != selected.VersionID {
			t.Errorf("healthy 304 lost selected identity: %v", response.Header)
		}
		return
	}
	if response.Header.Get("ETag") != "" || response.Header.Get("X-Version-Id") != "" || response.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("unbound 304 certified reusable identity: %v", response.Header)
	}
}

func TestThumbnailMediaRevalidationAuthorizationPrecedesDeclaration(t *testing.T) {
	for _, failure := range []error{nil, errors.New("revalidation authorization unavailable")} {
		f := newThumbnailMediaOpenFixture(t, "image/png", "image/webp", pngBytesAlt(t, 32, 16), true)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				provider := &readBoundaryProvider{rejectAt: 2, failure: failure}
				f.svc.WithAuthorizer(provider)
				response, body := thumbnailMediaRevalidationRequest(t, f, method, endpoint, map[string]string{"If-None-Match": "*"})
				want := http.StatusForbidden
				if failure != nil {
					want = http.StatusInternalServerError
				}
				if response.StatusCode != want || provider.reads.Load() != 2 || bytes.Contains(body, []byte("webp")) {
					t.Errorf("revalidation auth bypassed/leaked media: status=%d checks=%d body=%q", response.StatusCode, provider.reads.Load(), body)
				}
				assertThumbnailMediaOpenErrorHeaders(t, response)
				assertThumbnailMediaRevalidationNoPipeline(t, f)
			}
		}
	}
}

func checkThumbnailMediaRevalidation304(t *testing.T, f *thumbnailEncodingOpenFixture, method, endpoint string, headers map[string]string, selected repository.Object, bound bool) {
	t.Helper()
	before := snapshotThumbCounters(t)
	before304 := thumbnail304Counter(t)
	response, body := thumbnailMediaRevalidationRequest(t, f, method, endpoint, headers)
	assertThumbnailMediaRevalidationHealthy304(t, f, response, body, selected, bound)
	assertThumbDeltas(t, before, "", 0)
	if thumbnail304Counter(t) != before304+1 {
		t.Error("healthy revalidation lost its one 304 outcome")
	}
}

func TestThumbnailMediaRevalidationGenericRemainsMetadataOnly(t *testing.T) {
	// Undecodable bytes intentionally demonstrate that metadata-only 304 is
	// not a physical-health/decode promise, even for a changed generic type.
	for _, contentType := range []string{"application/octet-stream", "", `image/png; broken="`} {
		for _, bound := range []bool{true, false} {
			f := newThumbnailMediaOpenFixture(t, "image/png", contentType, webpBytes, bound)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, endpoint := range f.paths() {
					for _, headers := range thumbnailMediaRevalidationConditions(f, bound) {
						checkThumbnailMediaRevalidation304(t, f, method, endpoint, headers, f.current, bound)
						assertThumbnailMediaRevalidationNoPipeline(t, f)
					}
				}
			}
		}
	}
}

func TestThumbnailMediaRevalidationEncodingPrecedesDeclaration(t *testing.T) {
	f := newThumbnailEncodingOpenFixture(t, "image/png", "", true)
	body := pngBytesAlt(t, 32, 16)
	var err error
	f.current, err = f.svc.Put(context.Background(), "", "", f.current.Key, bytes.NewReader(body), int64(len(body)),
		service.PutOptions{ContentType: "image/webp", ContentEncoding: "identity"})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			before := snapshotThumbCounters(t)
			before304 := thumbnail304Counter(t)
			response, body := thumbnailMediaRevalidationRequest(t, f, method, endpoint, map[string]string{"If-None-Match": "*"})
			assertThumbnailEncodingOpenRejected(t, response, body, method, "identity")
			assertThumbnailMediaRevalidationNoPipeline(t, f)
			assertThumbDeltas(t, before, "content_encoding", 0)
			if thumbnail304Counter(t) != before304 {
				t.Error("encoding rejection counted 304")
			}
		}
	}
}

func TestThumbnailMediaRevalidationHistoricalPinIgnoresCurrentDeclaration(t *testing.T) {
	for _, bound := range []bool{true, false} {
		f := newThumbnailMediaOpenFixture(t, "application/octet-stream", "image/webp", webpBytes, bound)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				checkThumbnailMediaRevalidation304(t, f, method, endpoint+"&version="+f.older.VersionID,
					map[string]string{"If-None-Match": "*"}, f.older, bound)
				if f.repo.calls.Load() != 0 {
					t.Errorf("historical pin consulted current row: %d", f.repo.calls.Load())
				}
			}
		}
	}
}

func TestThumbnailMediaRevalidationIndependentRequestRecovers(t *testing.T) {
	f := newThumbnailMediaOpenFixture(t, "image/png", "image/webp", pngBytesAlt(t, 32, 16), true)
	want := thumbnailMediaRejection{"image/webp", http.StatusUnsupportedMediaType, "UnsupportedMediaType", "unsupported_format", false}
	for _, endpoint := range f.paths() {
		checkThumbnailMediaRevalidationRejection(t, f, http.MethodGet, endpoint, map[string]string{"If-None-Match": "*"}, want)
	}
	f.current = putContentSelectionFixture(t, f.svc, f.current.Key, pngBytesAlt(t, 32, 16), "image/png")
	for _, endpoint := range f.paths() {
		checkThumbnailMediaRevalidation304(t, f, http.MethodGet, endpoint, map[string]string{"If-None-Match": "*"}, f.current, true)
		assertThumbnailMediaRevalidationNoPipeline(t, f)
	}
}

func TestThumbnailMediaRevalidationPreflightRejectionIsTerminal(t *testing.T) {
	for _, check := range []struct {
		contentType string
		status      int
	}{{"text/plain", http.StatusBadRequest}, {"image/webp", http.StatusUnsupportedMediaType}} {
		f := newThumbnailMediaOpenFixture(t, check.contentType, "image/png", pngBytesAlt(t, 32, 16), true)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				response, _ := thumbnailMediaRevalidationRequest(t, f, method, endpoint, map[string]string{"If-None-Match": "*"})
				if response.StatusCode != check.status || f.repo.calls.Load() != 1 {
					t.Errorf("preflight rejection retried: status=%d stats=%d", response.StatusCode, f.repo.calls.Load())
				}
				assertThumbnailMediaOpenErrorHeaders(t, response)
				assertThumbnailMediaRevalidationNoSource(t, f)
			}
		}
	}
}

func TestThumbnailMediaRevalidationRejectionSkipsDecodeAdmission(t *testing.T) {
	f := newThumbnailMediaOpenFixture(t, "image/png", "image/webp", pngBytesAlt(t, 32, 16), true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := f.admission.Acquire(ctx, service.DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	want := thumbnailMediaRejection{"image/webp", http.StatusUnsupportedMediaType, "UnsupportedMediaType", "unsupported_format", false}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			checkThumbnailMediaRevalidationRejection(t, f, method, endpoint, map[string]string{"If-None-Match": "*"}, want)
		}
	}
}

func TestThumbnailMediaRevalidationOpenAPIDocumentsRejection(t *testing.T) {
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
		for _, status := range []string{"400", "415"} {
			description := spec.Paths[check.path][check.method].Responses[status].Description
			for _, phrase := range []string{"second authorized conditional Stat", "Content-Type", "not 304"} {
				if !strings.Contains(description, phrase) {
					t.Errorf("%s %s missing %s media revalidation contract %q: %q", check.method, check.path, status, phrase, description)
				}
			}
		}
	}
}
