package rest

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type contentHeaderFixture struct {
	svc     *service.FileService
	store   *openedContentStore
	web     *httptest.Server
	older   repository.Object
	current repository.Object
}

func newContentHeaderFixture(t *testing.T, key string) *contentHeaderFixture {
	t.Helper()
	_, repo, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := &openedContentStore{Storage: local}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	older := putContentHeaderVersion(t, svc, key, "old", []byte("<h1>old attachment</h1>"), "identity")
	current := putContentHeaderVersion(t, svc, key, "new", []byte("<h1>current attachment</h1>"), "identity")
	return &contentHeaderFixture{svc: svc, store: store, older: older, current: current, web: serveLegacyDenialFixture(t, svc, repo)}
}

func putContentHeaderVersion(t *testing.T, svc *service.FileService, key, label string, data []byte, encoding string) repository.Object {
	t.Helper()
	digest := md5.Sum(data)
	ctx := access.WithPrincipal(context.Background(), access.Principal{
		SubjectID: "reader", TenantID: "default", Kind: access.PrincipalUser, Scopes: []string{"read", "write"},
	})
	class := "STANDARD_IA"
	if label == "old" {
		class = "ONEZONE_IA"
	}
	object, err := svc.Put(ctx, "", "", key, bytes.NewReader(data), int64(len(data)), service.PutOptions{
		ContentType: "text/html; charset=utf-8", Metadata: map[string]string{"label": label}, StorageClass: class,
		ContentMD5: base64.StdEncoding.EncodeToString(digest[:]), ContentEncoding: encoding,
		ContentDisposition: `attachment; filename="` + label + `.html"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func contentHeaderPaths(fixture *contentHeaderFixture, selected repository.Object, pinned bool) []string {
	version := ""
	if pinned {
		version = selected.VersionID
	}
	return readBoundaryContentPaths(fixture.web.URL+"/v1", selected.Key, version)
}

func contentHeaderRequest(t *testing.T, method, endpoint string, extra map[string]string) (*http.Response, []byte) {
	t.Helper()
	// Raw representation headers/bytes, not net/http's implicit gzip decoding.
	headers := map[string]string{"Authorization": "Bearer reader", "Accept-Encoding": "identity"}
	for name, value := range extra {
		headers[name] = value
	}
	return req(t, method, endpoint, nil, headers)
}

func assertContentHeaderMetadata(t *testing.T, response *http.Response, selected repository.Object, pinned bool) {
	t.Helper()
	expected := map[string]string{
		"ETag": `"` + selected.ETag + `"`, "Last-Modified": selected.UpdatedAt.UTC().Format(http.TimeFormat),
		"Content-Disposition": selected.Metadata["_aero_content_disposition"],
		"Content-Encoding":    selected.Metadata["_aero_content_encoding"], "X-Content-MD5": selected.Metadata["_aero_content_md5"],
		"X-Storage-Class": selected.StorageClass, "X-Meta-Label": selected.Metadata["label"], "X-Version-Id": "",
	}
	if pinned {
		expected["X-Version-Id"] = selected.VersionID
	}
	for name, want := range expected {
		if got := response.Header.Get(name); got != want {
			t.Errorf("selected metadata lost/mixed: status=%d header=%s got=%q want=%q", response.StatusCode, name, got, want)
		}
	}
	for name := range response.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-meta-_aero_") {
			t.Errorf("reserved metadata serialized: %s", name)
		}
	}
}

func assertContentHeaderStream(t *testing.T, fixture *contentHeaderFixture, selected repository.Object, opens, closes int64) {
	t.Helper()
	if fixture.store.opens.Load() != opens || fixture.store.closes.Load() != closes || fixture.store.key.Load() != selected.StorageKey {
		t.Fatalf("response changed source or leaked it: opens=%d closes=%d key=%v want=%s", fixture.store.opens.Load(), fixture.store.closes.Load(), fixture.store.key.Load(), selected.StorageKey)
	}
}

func TestContentReadHeadersGetHeadParity(t *testing.T) {
	for _, key := range []string{"literal/report.html", "literal/thumbnail"} {
		fixture := newContentHeaderFixture(t, key)
		for _, selected := range []repository.Object{fixture.current, fixture.older} {
			pinned := selected.VersionID == fixture.older.VersionID
			for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
				opens, closes := fixture.store.opens.Load(), fixture.store.closes.Load()
				get, body := contentHeaderRequest(t, http.MethodGet, endpoint, nil)
				head, empty := contentHeaderRequest(t, http.MethodHead, endpoint, nil)
				if get.StatusCode != http.StatusOK || head.StatusCode != http.StatusOK || int64(len(body)) != selected.Size || len(empty) != 0 {
					t.Fatalf("GET/HEAD failed: get=%d head=%d bytes=%d/%d", get.StatusCode, head.StatusCode, len(body), len(empty))
				}
				assertContentHeaderMetadata(t, get, selected, pinned)
				assertContentHeaderMetadata(t, head, selected, pinned)
				assertContentHeaderStream(t, fixture, selected, opens+1, closes+1)
				if get.Header.Get("Content-Length") != head.Header.Get("Content-Length") || head.Header.Get("Content-Type") != selected.ContentType {
					t.Errorf("HEAD does not describe GET: get=%v head=%v", get.Header, head.Header)
				}
			}
		}
	}
}

func TestContentReadHeadersPartialAndIfRange(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, matching := range []bool{false, true} {
				value, status, length := `"wrong"`, http.StatusOK, selected.Size
				if matching {
					value, status, length = `"`+selected.ETag+`"`, http.StatusPartialContent, 4
				}
				opens, closes := fixture.store.opens.Load(), fixture.store.closes.Load()
				response, body := contentHeaderRequest(t, http.MethodGet, endpoint, map[string]string{"Range": "bytes=0-3", "If-Range": value})
				if response.StatusCode != status || int64(len(body)) != length || response.Header.Get("Content-Length") != strconv.FormatInt(length, 10) {
					t.Fatalf("range response changed: status=%d want=%d bytes=%d want=%d headers=%v", response.StatusCode, status, len(body), length, response.Header)
				}
				assertContentHeaderMetadata(t, response, selected, pinned)
				assertContentHeaderStream(t, fixture, selected, opens+1, closes+1)
			}
		}
	}
}

func TestContentReadHeadersConditionalGetHeadParity(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				opens := fixture.store.opens.Load()
				response, body := contentHeaderRequest(t, method, endpoint, map[string]string{"If-None-Match": `"` + selected.ETag + `"`})
				if response.StatusCode != http.StatusNotModified || len(body) != 0 {
					t.Fatalf("conditional content changed: method=%s status=%d body=%q", method, response.StatusCode, body)
				}
				assertContentHeaderMetadata(t, response, selected, pinned)
				if pinned && method == http.MethodGet {
					opens++ // Existing historical GET opens before its validator check.
				}
				if fixture.store.opens.Load() != opens || fixture.store.closes.Load() != opens {
					t.Errorf("304/HEAD added an open/leaked source: opens=%d closes=%d want=%d", fixture.store.opens.Load(), fixture.store.closes.Load(), opens)
				}
			}
		}
	}
}

func TestContentReadHeadersEncodedHistoricalAttachment(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	var data bytes.Buffer
	writer := gzip.NewWriter(&data)
	if _, err := writer.Write([]byte("<h1>encoded old attachment</h1>")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := putContentHeaderVersion(t, fixture.svc, fixture.older.Key, "encoded", data.Bytes(), "gzip")
	putContentHeaderVersion(t, fixture.svc, encoded.Key, "latest", []byte("new plaintext"), "identity")
	for _, endpoint := range contentHeaderPaths(fixture, encoded, true) {
		response, body := contentHeaderRequest(t, http.MethodGet, endpoint, nil)
		if response.StatusCode != http.StatusOK || !bytes.Equal(body, data.Bytes()) || response.Header.Get("Content-Length") != strconv.Itoa(data.Len()) {
			t.Fatalf("historical encoded bytes changed: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
		}
		assertContentHeaderMetadata(t, response, encoded, true)
	}
}

func TestContentReadHeadersOpenAPIDocumentsStoredMetadata(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	path := spec["paths"].(map[string]any)["/v1/files/{key}"].(map[string]any)
	for _, method := range []string{"get", "head"} {
		operation := path[method].(map[string]any)
		response := operation["responses"].(map[string]any)["304"].(map[string]any)
		for _, field := range []string{"attachment", "encoding", "digest", "storage class", "user metadata"} {
			if !strings.Contains(response["description"].(string), field) {
				t.Errorf("%s 304 missing stored %s contract", method, field)
			}
		}
	}
}

func TestContentReadHeadersFailedConditionsDoNotExposeMetadata(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, test := range []struct {
				headers map[string]string
				status  int
			}{
				{map[string]string{"If-Match": `"wrong"`, "Range": "bytes=0-3"}, http.StatusPreconditionFailed},
				{map[string]string{"Range": "bytes=99999-"}, http.StatusRequestedRangeNotSatisfiable},
			} {
				response, body := contentHeaderRequest(t, http.MethodGet, endpoint, test.headers)
				if response.StatusCode != test.status {
					t.Fatalf("error status changed: status=%d want=%d body=%q", response.StatusCode, test.status, body)
				}
				assertNoContentHeaderMetadata(t, response)
			}
		}
	}
}

func assertNoContentHeaderMetadata(t *testing.T, response *http.Response) {
	t.Helper()
	for _, name := range []string{"ETag", "X-Version-Id", "Content-Disposition", "Content-Encoding", "X-Content-MD5", "X-Storage-Class", "X-Meta-Label"} {
		if response.Header.Get(name) != "" {
			t.Errorf("error response exposed %s=%s", name, response.Header.Get(name))
		}
	}
}

func TestContentReadHeadersDeniedResponseDoesNotExposeMetadata(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				fixture.svc.WithAuthorizer(&readBoundaryProvider{rejectAt: 1})
				response, body := contentHeaderRequest(t, method, endpoint, map[string]string{"If-None-Match": "*", "Range": "bytes=0-3"})
				if response.StatusCode != http.StatusForbidden || fixture.store.opens.Load() != 0 {
					t.Fatalf("denied metadata returned/opened content: status=%d opens=%d body=%q", response.StatusCode, fixture.store.opens.Load(), body)
				}
				assertNoContentHeaderMetadata(t, response)
			}
		}
	}
}
