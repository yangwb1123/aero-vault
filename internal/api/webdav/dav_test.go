// Package webdav_test provides integration tests for the WebDAV handler.
// Tests drive the handler over real HTTP using httptest and the full service
// stack (SQLite repo + local storage) so every code path in dav.go is
// exercised end-to-end.
package webdav_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/api/webdav"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// newTestServer spins up a fresh WebDAV handler backed by a temp SQLite
// database and a temp local-storage root.  It returns the httptest.Server
// and a cleanup function (also registered via t.Cleanup).
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := newTestServerWithSvc(t)
	return srv
}

// newTestServerSvc is a backward-compatible alias for newTestServerWithSvc.
func newTestServerSvc(t *testing.T) (*httptest.Server, *service.FileService) {
	return newTestServerWithSvc(t)
}

// newTestServerWithSvc is newTestServer plus a handle to the underlying
// FileService, for tests that seed or inspect object metadata (ContentType/
// Metadata/Tags) the WebDAV layer does not expose over HTTP.
func newTestServerWithSvc(t *testing.T) (*httptest.Server, *service.FileService) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("repository.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("repo.Migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	svc := service.NewFileService(store, repo, nil)
	// Wrap with the Tenant middleware exactly as cmd/server does: the WebDAV
	// handler reads the tenant from the request context, which that middleware
	// populates from the X-Aero-Tenant header (defaulting to "default").
	h := mw.Tenant(webdav.Handler("/webdav", svc, nil))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, svc
}

// do sends a request to the server and returns the response plus body bytes.
func do(t *testing.T, srv *httptest.Server, method, path string, body []byte, hdrs map[string]string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, path, err)
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// ----------------------------------------------------------------------------
// Lifecycle: PUT → GET → HEAD → DELETE
// ----------------------------------------------------------------------------

func TestPutGetHead(t *testing.T) {
	srv := newTestServer(t)
	const content = "hello webdav"

	// PUT creates the resource.
	resp, _ := do(t, srv, http.MethodPut, "/webdav/hello.txt", []byte(content), nil)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT: got %d, want 201 or 204", resp.StatusCode)
	}

	// GET returns the body.
	resp, body := do(t, srv, http.MethodGet, "/webdav/hello.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: got %d, want 200", resp.StatusCode)
	}
	if string(body) != content {
		t.Fatalf("GET body: got %q, want %q", string(body), content)
	}

	// HEAD returns 200 with Content-Length but no body.
	resp, headBody := do(t, srv, http.MethodHead, "/webdav/hello.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: got %d, want 200", resp.StatusCode)
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD body should be empty, got %q", headBody)
	}
}

func TestPutOverwrite(t *testing.T) {
	srv := newTestServer(t)

	do(t, srv, http.MethodPut, "/webdav/over.txt", []byte("v1"), nil)
	do(t, srv, http.MethodPut, "/webdav/over.txt", []byte("v2 updated"), nil)

	resp, body := do(t, srv, http.MethodGet, "/webdav/over.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after overwrite: %d", resp.StatusCode)
	}
	if string(body) != "v2 updated" {
		t.Fatalf("content after overwrite: got %q want %q", string(body), "v2 updated")
	}
}

func TestDeleteRemovesResource(t *testing.T) {
	srv := newTestServer(t)

	do(t, srv, http.MethodPut, "/webdav/gone.txt", []byte("bye"), nil)
	resp, _ := do(t, srv, http.MethodDelete, "/webdav/gone.txt", nil, nil)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: got %d, want 204 or 200", resp.StatusCode)
	}

	// Subsequent GET must return 404.
	resp, _ = do(t, srv, http.MethodGet, "/webdav/gone.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE: got %d, want 404", resp.StatusCode)
	}
}

func TestGetMissing(t *testing.T) {
	srv := newTestServer(t)

	resp, _ := do(t, srv, http.MethodGet, "/webdav/notexist.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET missing: got %d, want 404", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// PROPFIND — single file and collection
// ----------------------------------------------------------------------------

const propfindAllprop = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`

func TestPropfindDepth0File(t *testing.T) {
	srv := newTestServer(t)

	do(t, srv, http.MethodPut, "/webdav/doc.txt", []byte("data"), nil)

	resp, body := do(t, srv, "PROPFIND", "/webdav/doc.txt", []byte(propfindAllprop), map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND depth=0: got %d, want 207", resp.StatusCode)
	}
	if !strings.Contains(string(body), "doc.txt") {
		t.Fatalf("PROPFIND body missing filename; body=%q", string(body))
	}
}

func TestPropfindDepth1Root(t *testing.T) {
	srv := newTestServer(t)

	// Upload two files so the root has something to list.
	do(t, srv, http.MethodPut, "/webdav/a.txt", []byte("aaa"), nil)
	do(t, srv, http.MethodPut, "/webdav/b.txt", []byte("bbb"), nil)

	resp, body := do(t, srv, "PROPFIND", "/webdav/", []byte(propfindAllprop), map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND depth=1 root: got %d, want 207", resp.StatusCode)
	}
	if !strings.Contains(string(body), "a.txt") || !strings.Contains(string(body), "b.txt") {
		t.Fatalf("PROPFIND root missing files; body=%q", string(body))
	}
}

// propfindResponse is a minimal struct for parsing WebDAV multi-status XML.
type propfindResponse struct {
	XMLName   xml.Name `xml:"multistatus"`
	Responses []struct {
		Href string `xml:"href"`
	} `xml:"response"`
}

func TestPropfindXMLWellFormed(t *testing.T) {
	srv := newTestServer(t)
	do(t, srv, http.MethodPut, "/webdav/xmltest.txt", []byte("xmlcontent"), nil)

	_, body := do(t, srv, "PROPFIND", "/webdav/xmltest.txt", []byte(propfindAllprop), map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml",
	})
	var ms propfindResponse
	if err := xml.Unmarshal(body, &ms); err != nil {
		t.Fatalf("PROPFIND response is not valid XML: %v\nbody=%q", err, string(body))
	}
	if len(ms.Responses) == 0 {
		t.Fatalf("PROPFIND returned no <response> elements")
	}
}

// ----------------------------------------------------------------------------
// MKCOL — create a virtual collection
// ----------------------------------------------------------------------------

func TestMKCOLAndPropfind(t *testing.T) {
	srv := newTestServer(t)

	// MKCOL is a no-op in davFS (virtual dirs), so just assert no 5xx.
	resp, _ := do(t, srv, "MKCOL", "/webdav/mydir/", nil, nil)
	if resp.StatusCode >= 500 {
		t.Fatalf("MKCOL: got %d, want < 500", resp.StatusCode)
	}

	// Put a file under the "directory".
	do(t, srv, http.MethodPut, "/webdav/mydir/file.txt", []byte("nested"), nil)

	// GET the file under the dir.
	resp, body := do(t, srv, http.MethodGet, "/webdav/mydir/file.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET nested file: %d", resp.StatusCode)
	}
	if string(body) != "nested" {
		t.Fatalf("GET nested file body: got %q want %q", string(body), "nested")
	}
}

func TestPropfindDepth1Dir(t *testing.T) {
	// davFS.Stat mirrors OpenFile's trailing-"/" fast-path, so a PROPFIND on a
	// subdirectory ("dir/") resolves as a collection and lists its members.
	srv := newTestServer(t)

	do(t, srv, http.MethodPut, "/webdav/dir/x.txt", []byte("x"), nil)
	do(t, srv, http.MethodPut, "/webdav/dir/y.txt", []byte("y"), nil)

	resp, body := do(t, srv, "PROPFIND", "/webdav/dir/", []byte(propfindAllprop), map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND dir depth=1: got %d, want 207", resp.StatusCode)
	}
	if !strings.Contains(string(body), "x.txt") || !strings.Contains(string(body), "y.txt") {
		t.Fatalf("PROPFIND dir missing files; body=%q", string(body))
	}
}

// ----------------------------------------------------------------------------
// MOVE — rename via copy-then-delete
// ----------------------------------------------------------------------------

func TestMoveRenamesFile(t *testing.T) {
	srv := newTestServer(t)

	do(t, srv, http.MethodPut, "/webdav/src.txt", []byte("move me"), nil)

	resp, _ := do(t, srv, "MOVE", "/webdav/src.txt", nil, map[string]string{
		"Destination": srv.URL + "/webdav/dst.txt",
		"Overwrite":   "T",
	})
	// golang.org/x/net/webdav MOVE returns 201 or 204.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("MOVE: got %d, want 201 or 204", resp.StatusCode)
	}

	// Old path must be gone.
	resp, _ = do(t, srv, http.MethodGet, "/webdav/src.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after MOVE (old path): got %d, want 404", resp.StatusCode)
	}

	// New path has the content.
	resp, body := do(t, srv, http.MethodGet, "/webdav/dst.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after MOVE (new path): got %d, want 200", resp.StatusCode)
	}
	if string(body) != "move me" {
		t.Fatalf("MOVE: content at dst: got %q, want %q", string(body), "move me")
	}
}

// TestMoveLargeFile renames an object above the 8 MiB spill threshold, so the
// copy inside Rename must go through the temp-file-backed spill path rather
// than buffering the whole payload in memory.
func TestMoveLargeFile(t *testing.T) {
	srv := newTestServer(t)

	const size = 9 << 20
	want := make([]byte, size)
	for i := range want {
		want[i] = byte((i*2654435761 + 1013904223) >> 13)
	}

	resp, _ := do(t, srv, http.MethodPut, "/webdav/big-src.bin", want, nil)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT big-src: got %d, want 201 or 204", resp.StatusCode)
	}

	resp, _ = do(t, srv, "MOVE", "/webdav/big-src.bin", nil, map[string]string{
		"Destination": srv.URL + "/webdav/big-dst.bin",
		"Overwrite":   "T",
	})
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("MOVE large: got %d, want 201 or 204", resp.StatusCode)
	}

	resp, _ = do(t, srv, http.MethodGet, "/webdav/big-src.bin", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after MOVE (old path): got %d, want 404", resp.StatusCode)
	}

	resp, body := do(t, srv, http.MethodGet, "/webdav/big-dst.bin", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after MOVE (new path): got %d, want 200", resp.StatusCode)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("MOVE large: bytes at dst differ from source (%d vs %d bytes)", len(body), len(want))
	}
}

// TestMovePreservesMetadata seeds an object (via the service, since the WebDAV
// PUT path does not let clients set ContentType/Metadata/Tags) with a
// non-default Content-Type, a user metadata entry, and a tag, then MOVEs it and
// asserts those survived on the destination. The bytes must also be intact.
func TestMovePreservesMetadata(t *testing.T) {
	srv, svc := newTestServerSvc(t)

	const content = "carry me across"
	const ctype = "application/x-aero-test"
	wantMeta := map[string]string{"author": "amelia", "purpose": "rename-test"}
	wantTags := map[string]string{"env": "staging"}

	if _, err := svc.Put(context.Background(), service.DefaultTenant, service.DefaultBucket,
		"meta-src.txt", bytes.NewReader([]byte(content)), int64(len(content)),
		service.PutOptions{ContentType: ctype, Metadata: wantMeta, Tags: wantTags}); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	resp, _ := do(t, srv, "MOVE", "/webdav/meta-src.txt", nil, map[string]string{
		"Destination": srv.URL + "/webdav/meta-dst.txt",
		"Overwrite":   "T",
	})
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("MOVE: got %d, want 201 or 204", resp.StatusCode)
	}

	// Old path must be gone.
	resp, _ = do(t, srv, http.MethodGet, "/webdav/meta-src.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after MOVE (old path): got %d, want 404", resp.StatusCode)
	}

	// New path retains the bytes.
	resp, body := do(t, srv, http.MethodGet, "/webdav/meta-dst.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after MOVE (new path): got %d, want 200", resp.StatusCode)
	}
	if string(body) != content {
		t.Fatalf("MOVE: content at dst: got %q, want %q", string(body), content)
	}

	// And the metadata that the WebDAV layer cannot surface over HTTP: inspect
	// the destination object through the service directly.
	obj, err := svc.Stat(context.Background(), service.DefaultTenant, service.DefaultBucket, "meta-dst.txt")
	if err != nil {
		t.Fatalf("Stat dst: %v", err)
	}
	if obj.ContentType != ctype {
		t.Errorf("ContentType after MOVE: got %q, want %q", obj.ContentType, ctype)
	}
	for k, v := range wantMeta {
		if obj.Metadata[k] != v {
			t.Errorf("Metadata[%q] after MOVE: got %q, want %q", k, obj.Metadata[k], v)
		}
	}
	for k, v := range wantTags {
		if obj.Tags[k] != v {
			t.Errorf("Tags[%q] after MOVE: got %q, want %q", k, obj.Tags[k], v)
		}
	}
}

// TestMoveMissingSource asserts Rename's not-found error keeps surfacing the
// same way: moveFiles in golang.org/x/net/webdav maps any Rename error to 403.
func TestMoveMissingSource(t *testing.T) {
	srv := newTestServer(t)

	resp, _ := do(t, srv, "MOVE", "/webdav/no-such-src.txt", nil, map[string]string{
		"Destination": srv.URL + "/webdav/no-such-dst.txt",
		"Overwrite":   "T",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("MOVE missing source: got %d, want 403", resp.StatusCode)
	}

	// The destination must not have been created.
	resp, _ = do(t, srv, http.MethodGet, "/webdav/no-such-dst.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET dst after failed MOVE: got %d, want 404", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// OPTIONS — DAV capability advertisement
// ----------------------------------------------------------------------------

func TestOptionsAdvertisesDAV(t *testing.T) {
	srv := newTestServer(t)

	resp, _ := do(t, srv, http.MethodOptions, "/webdav/", nil, nil)
	// OPTIONS should succeed (200 or 204) and include a DAV header.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS: got %d, want 200 or 204", resp.StatusCode)
	}
	dav := resp.Header.Get("DAV")
	if !strings.Contains(dav, "1") {
		t.Fatalf("OPTIONS: DAV header %q should contain '1'", dav)
	}
}

// ----------------------------------------------------------------------------
// Tenant routing via X-Aero-Tenant header
// ----------------------------------------------------------------------------

func TestTenantIsolation(t *testing.T) {
	// The WebDAV handler is tenant-aware via the request context. In production
	// (and in newTestServer) the Tenant middleware populates that context from
	// the X-Aero-Tenant header, so two tenants get isolated namespaces.
	srv := newTestServer(t)

	// Upload "hello" for tenant-a.
	do(t, srv, http.MethodPut, "/webdav/shared.txt", []byte("tenant-a data"),
		map[string]string{"X-Aero-Tenant": "tenant-a"})

	// tenant-b must not see it.
	resp, _ := do(t, srv, http.MethodGet, "/webdav/shared.txt", nil,
		map[string]string{"X-Aero-Tenant": "tenant-b"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("tenant-b GET tenant-a file: got %d, want 404", resp.StatusCode)
	}

	// tenant-a can retrieve it.
	resp, body := do(t, srv, http.MethodGet, "/webdav/shared.txt", nil,
		map[string]string{"X-Aero-Tenant": "tenant-a"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tenant-a GET own file: got %d, want 200", resp.StatusCode)
	}
	if string(body) != "tenant-a data" {
		t.Fatalf("tenant-a GET body: got %q want %q", string(body), "tenant-a data")
	}
}

// ----------------------------------------------------------------------------
// Default-tenant fallback (no X-Aero-Tenant header → "default")
// ----------------------------------------------------------------------------

func TestDefaultTenantFallback(t *testing.T) {
	srv := newTestServer(t)

	do(t, srv, http.MethodPut, "/webdav/default.txt", []byte("default content"), nil)

	resp, body := do(t, srv, http.MethodGet, "/webdav/default.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET default tenant: %d", resp.StatusCode)
	}
	if string(body) != "default content" {
		t.Fatalf("GET default tenant body: got %q", body)
	}
}

// ----------------------------------------------------------------------------
// Large-ish body: ensure buffering in davWriter / davReader works end-to-end
// ----------------------------------------------------------------------------

func TestLargeFile(t *testing.T) {
	srv := newTestServer(t)

	large := strings.Repeat("x", 256*1024) // 256 KiB

	resp, _ := do(t, srv, http.MethodPut, "/webdav/large.bin", []byte(large), nil)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT large: %d", resp.StatusCode)
	}

	resp, body := do(t, srv, http.MethodGet, "/webdav/large.bin", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET large: %d", resp.StatusCode)
	}
	if len(body) != len(large) {
		t.Fatalf("large file: got %d bytes, want %d", len(body), len(large))
	}
}

// ----------------------------------------------------------------------------
// Spill-to-disk round trip: an object larger than the 8 MiB spill threshold
// must PUT and GET byte-for-byte, exercising the temp-file path in both the
// write (davWriter) and read (davReader) directions.
// ----------------------------------------------------------------------------

func TestSpillFileRoundTrip(t *testing.T) {
	srv := newTestServer(t)

	// 9 MiB of deterministic pseudo-random bytes — above the default 8 MiB
	// spillThreshold so both the upload and download paths spill to a temp
	// file rather than staying in memory.
	const size = 9 << 20
	want := make([]byte, size)
	for i := range want {
		want[i] = byte((i*2654435761 + 1013904223) >> 13)
	}

	resp, _ := do(t, srv, http.MethodPut, "/webdav/spill.bin", want, nil)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT spill: got %d, want 201 or 204", resp.StatusCode)
	}

	resp, body := do(t, srv, http.MethodGet, "/webdav/spill.bin", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET spill: got %d, want 200", resp.StatusCode)
	}
	if len(body) != len(want) {
		t.Fatalf("spill file: got %d bytes, want %d", len(body), len(want))
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("spill file: downloaded bytes differ from uploaded bytes")
	}
}

// TestSpillRangeRequest confirms Range/Seek still works on a spilled object:
// the read path must serve a byte range out of the temp-file-backed buffer.
func TestSpillRangeRequest(t *testing.T) {
	srv := newTestServer(t)

	const size = 9 << 20
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i)
	}
	resp, _ := do(t, srv, http.MethodPut, "/webdav/range.bin", want, nil)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT range: got %d, want 201 or 204", resp.StatusCode)
	}

	// Request bytes 1000000-1000099 (100 bytes well past the spill threshold).
	const start, end = 1_000_000, 1_000_099
	resp, body := do(t, srv, http.MethodGet, "/webdav/range.bin", nil, map[string]string{
		"Range": "bytes=1000000-1000099",
	})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("GET range: got %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(body, want[start:end+1]) {
		t.Fatalf("range bytes differ from source slice")
	}
}

// ----------------------------------------------------------------------------
// COPY — golang.org/x/net/webdav maps COPY to Rename internally; assert
// current behaviour (may return 201 or 204 if supported, or a client error if
// not — but must not panic or 5xx).
// ----------------------------------------------------------------------------

func TestCopyDoesNotPanic(t *testing.T) {
	srv := newTestServer(t)
	do(t, srv, http.MethodPut, "/webdav/orig.txt", []byte("original"), nil)

	resp, _ := do(t, srv, "COPY", "/webdav/orig.txt", nil, map[string]string{
		"Destination": srv.URL + "/webdav/copy.txt",
	})
	// COPY may not be implemented; assert it doesn't 5xx or panic.
	if resp.StatusCode >= 500 {
		t.Fatalf("COPY returned server error: %d", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// PROPFIND on a non-existent path — must return 404, not 5xx
// ----------------------------------------------------------------------------

func TestPropfindMissingPath(t *testing.T) {
	srv := newTestServer(t)

	resp, _ := do(t, srv, "PROPFIND", "/webdav/nonexistent.txt", []byte(propfindAllprop), map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml",
	})
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("PROPFIND missing: got 500, want 404")
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Logf("PROPFIND missing: got %d (documenting current behaviour)", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// Stat root — the root ("/") must always be a valid directory
// ----------------------------------------------------------------------------

func TestPropfindRootIsDir(t *testing.T) {
	srv := newTestServer(t)

	resp, body := do(t, srv, "PROPFIND", "/webdav/", []byte(propfindAllprop), map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND root: got %d want 207; body=%q", resp.StatusCode, body)
	}
}

// ----------------------------------------------------------------------------
// Multiple files: ensure List does not confuse keys across different files
// ----------------------------------------------------------------------------

func TestMultipleFilesDistinct(t *testing.T) {
	srv := newTestServer(t)

	files := map[string]string{
		"/webdav/alpha.txt": "AAA",
		"/webdav/beta.txt":  "BBB",
		"/webdav/gamma.txt": "CCC",
	}
	for path, content := range files {
		do(t, srv, http.MethodPut, path, []byte(content), nil)
	}

	for path, want := range files {
		resp, body := do(t, srv, http.MethodGet, path, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", path, resp.StatusCode)
		}
		if string(body) != want {
			t.Fatalf("GET %s: got %q want %q", path, string(body), want)
		}
	}
}

// ----------------------------------------------------------------------------
// DELETE of non-existent resource — should not 5xx (behaviour may be 404 or
// 409; we just assert not 5xx per WebDAV spec)
// ----------------------------------------------------------------------------

func TestDeleteMissingResource(t *testing.T) {
	srv := newTestServer(t)

	resp, _ := do(t, srv, http.MethodDelete, "/webdav/doesnotexist.txt", nil, nil)
	if resp.StatusCode >= 500 {
		t.Fatalf("DELETE missing: got %d, want < 500", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// Stored Content-Type (webdav.ContentTyper): a GET must serve the content-type
// the client stored via REST/S3 PUT, not one sniffed from the bytes.
// ----------------------------------------------------------------------------

func TestGetServesStoredContentType(t *testing.T) {
	srv, svc := newTestServerWithSvc(t)

	const want = "application/x-custom"
	if _, err := svc.Put(context.Background(), "default", service.DefaultBucket, "custom.bin",
		bytes.NewReader([]byte("payload")), int64(len("payload")),
		service.PutOptions{ContentType: want}); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	resp, _ := do(t, srv, http.MethodGet, "/webdav/custom.bin", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: got %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != want {
		t.Fatalf("Content-Type: got %q, want stored %q", got, want)
	}
}

// TestGetStoredContentTypeBeatsSniff seeds an object whose bytes would sniff to
// a different type (HTML) than its stored Content-Type, proving the stored type
// wins over x/net/webdav's http.DetectContentType fallback.
func TestGetStoredContentTypeBeatsSniff(t *testing.T) {
	srv, svc := newTestServerWithSvc(t)

	const stored = "application/x-custom"
	body := []byte("<!DOCTYPE html><html><body>hi</body></html>")
	if _, err := svc.Put(context.Background(), "default", service.DefaultBucket, "looks-like-html",
		bytes.NewReader(body), int64(len(body)),
		service.PutOptions{ContentType: stored}); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	resp, got := do(t, srv, http.MethodGet, "/webdav/looks-like-html", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != stored {
		t.Fatalf("Content-Type: got %q, want stored %q (must not sniff to text/html)", ct, stored)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("GET body: got %q, want %q", got, body)
	}
}

// ----------------------------------------------------------------------------
// Readdir pagination — a directory with more than maxListPage (1000) objects
// must list completely. PROPFIND Depth:1 walks the collection via Readdir(0),
// whose contract is to return EVERY entry; before the pagination fix the listing
// was hard-capped at the first 1000 rows and silently truncated.
// ----------------------------------------------------------------------------

func TestPropfindListsBeyondOnePage(t *testing.T) {
	srv, svc := newTestServerWithSvc(t)

	// Seed 1001 objects so the listing spans two List() pages (maxListPage=1000).
	// Zero-padded names sort lexicographically, so big-1000.txt is the very last
	// row and would be dropped by a single-page (limit 1000) listing.
	const n = 1001
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("big/f%04d.txt", i)
		if _, err := svc.Put(context.Background(), service.DefaultTenant, service.DefaultBucket,
			key, strings.NewReader("x"), 1, service.PutOptions{}); err != nil {
			t.Fatalf("seed Put %s: %v", key, err)
		}
	}

	resp, body := do(t, srv, "PROPFIND", "/webdav/big/", []byte(propfindAllprop), map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND big/ depth=1: got %d, want 207", resp.StatusCode)
	}

	var ms propfindResponse
	if err := xml.Unmarshal(body, &ms); err != nil {
		t.Fatalf("PROPFIND response not valid XML: %v", err)
	}
	// Depth:1 yields the collection itself plus one <response> per member, so the
	// total must be n+1. A truncated listing would fall short of this.
	if got := len(ms.Responses); got != n+1 {
		t.Fatalf("PROPFIND big/: got %d <response> elements, want %d (collection + %d members)", got, n+1, n)
	}
	// The last-sorted file must appear: it is exactly the entry the old 1000-cap
	// would have dropped.
	if !strings.Contains(string(body), "f1000.txt") {
		t.Fatalf("PROPFIND big/: last entry f1000.txt missing — listing was truncated")
	}
	// And the first must still be there (no off-by-one at the page head).
	if !strings.Contains(string(body), "f0000.txt") {
		t.Fatalf("PROPFIND big/: first entry f0000.txt missing")
	}
}

// TestPropfindCollapsesNestedDirsAcrossPages confirms a virtual subdirectory
// whose objects straddle the maxListPage page boundary is emitted exactly once,
// not duplicated, when the listing paginates.
func TestPropfindCollapsesNestedDirsAcrossPages(t *testing.T) {
	srv, svc := newTestServerWithSvc(t)

	// 1001 files all under root/sub/ — they collapse to a single virtual dir
	// "sub" but their object keys span two List() pages.
	const n = 1001
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("root/sub/f%04d.txt", i)
		if _, err := svc.Put(context.Background(), service.DefaultTenant, service.DefaultBucket,
			key, strings.NewReader("y"), 1, service.PutOptions{}); err != nil {
			t.Fatalf("seed Put %s: %v", key, err)
		}
	}

	resp, body := do(t, srv, "PROPFIND", "/webdav/root/", []byte(propfindAllprop), map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND root/ depth=1: got %d, want 207", resp.StatusCode)
	}

	var ms propfindResponse
	if err := xml.Unmarshal(body, &ms); err != nil {
		t.Fatalf("PROPFIND response not valid XML: %v", err)
	}
	// Collection + exactly one collapsed "sub" entry == 2 responses. A double
	// emit across the page boundary would push this to 3.
	if got := len(ms.Responses); got != 2 {
		t.Fatalf("PROPFIND root/: got %d <response> elements, want 2 (collection + single collapsed sub/)", got)
	}
}

// ----------------------------------------------------------------------------
// MOVE rollback — copy-then-delete must not leave both names when the delete of
// the source fails after the destination is written.
// ----------------------------------------------------------------------------

// deleteFailStorage wraps a Storage and forces Delete to fail for any key whose
// path contains failOn, so a MOVE's source delete fails after the destination
// blob and row are already committed (the duplicate-leaving window).
type deleteFailStorage struct {
	storage.Storage
	failOn string
}

func (s *deleteFailStorage) Delete(ctx context.Context, key string) error {
	if strings.Contains(key, s.failOn) {
		return fmt.Errorf("injected delete failure for %q", key)
	}
	return s.Storage.Delete(ctx, key)
}

// newRollbackServer builds a WebDAV server whose storage fails Delete for any
// key containing failOn, returning the server plus the service for inspection.
func newRollbackServer(t *testing.T, failOn string) (*httptest.Server, *service.FileService) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("repository.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("repo.Migrate: %v", err)
	}
	base, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	store := &deleteFailStorage{Storage: base, failOn: failOn}
	svc := service.NewFileService(store, repo, nil)
	h := mw.Tenant(webdav.Handler("/webdav", svc, nil))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, svc
}

func TestMoveRollbackOnDeleteFailure(t *testing.T) {
	srv, svc := newRollbackServer(t, "rollback-src")

	const content = "do not duplicate me"
	if _, err := svc.Put(context.Background(), service.DefaultTenant, service.DefaultBucket,
		"rollback-src.txt", strings.NewReader(content), int64(len(content)),
		service.PutOptions{}); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	// MOVE: Put(dst) succeeds, Delete(src) fails (injected) → Rename returns an
	// error, which x/net/webdav maps to 403.
	resp, _ := do(t, srv, "MOVE", "/webdav/rollback-src.txt", nil, map[string]string{
		"Destination": srv.URL + "/webdav/rollback-dst.txt",
		"Overwrite":   "T",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("MOVE with failing source delete: got %d, want 403", resp.StatusCode)
	}

	// The destination must have been rolled back: it must NOT exist, otherwise
	// both names live on (the duplicate this fix prevents).
	if _, err := svc.Stat(context.Background(), service.DefaultTenant, service.DefaultBucket, "rollback-dst.txt"); err == nil {
		t.Fatalf("rollback-dst.txt still exists after failed MOVE — rollback did not run (duplicate)")
	}

	// The source remains intact (its delete failed before touching the repo row).
	if _, err := svc.Stat(context.Background(), service.DefaultTenant, service.DefaultBucket, "rollback-src.txt"); err != nil {
		t.Fatalf("rollback-src.txt should still exist after failed MOVE: %v", err)
	}
}
