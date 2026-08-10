package s3compat

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// S3 GetObject honors RFC 7232 conditionals: If-None-Match on a matching ETag ->
// 304; If-Match mismatch -> 412.
func TestS3GetObject_Conditional(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	// Create an object and capture its ETag.
	resp, _ := do(t, "PUT", base+"/b/c.txt", []byte("conditional body"), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		// fall back to a HEAD if PUT didn't echo it
		resp, _ = do(t, "HEAD", base+"/b/c.txt", nil, nil)
		etag = resp.Header.Get("ETag")
	}

	// If-None-Match with the current ETag => 304 Not Modified.
	resp, _ = do(t, "GET", base+"/b/c.txt", nil, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match match should be 304, got %d", resp.StatusCode)
	}

	// If-Match with a wrong ETag => 412 Precondition Failed.
	resp, _ = do(t, "GET", base+"/b/c.txt", nil, map[string]string{"If-Match": `"deadbeef"`})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match mismatch should be 412, got %d", resp.StatusCode)
	}

	// If-None-Match with a non-matching tag => normal 200 with body.
	resp, body := do(t, "GET", base+"/b/c.txt", nil, map[string]string{"If-None-Match": `"deadbeef"`})
	if resp.StatusCode != 200 || string(body) != "conditional body" {
		t.Fatalf("non-matching If-None-Match should serve 200, got %d %q", resp.StatusCode, body)
	}
}

func TestS3PutObject_Conditional(t *testing.T) {
	s := newTestServer(t)
	objectURL := s.URL + "/b/conditional-put.txt"
	resp, body := do(t, http.MethodPut, objectURL, []byte("version-one"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initial PUT status=%d body=%q", resp.StatusCode, body)
	}
	etag := resp.Header.Get("ETag")

	resp, _ = do(t, http.MethodPut, objectURL, []byte("blocked"), map[string]string{
		"If-None-Match": "*",
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-None-Match existing status=%d, want 412", resp.StatusCode)
	}
	assertS3Body(t, objectURL, "version-one")

	resp, _ = do(t, http.MethodPut, objectURL, []byte("blocked"), map[string]string{
		"If-Match": `"wrong"`,
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match stale status=%d, want 412", resp.StatusCode)
	}
	assertS3Body(t, objectURL, "version-one")

	resp, body = do(t, http.MethodPut, objectURL, []byte("version-two"), map[string]string{
		"If-Match": etag,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("If-Match current status=%d body=%q", resp.StatusCode, body)
	}
	assertS3Body(t, objectURL, "version-two")

	missingURL := s.URL + "/b/new.txt"
	resp, _ = do(t, http.MethodPut, missingURL, []byte("blocked"), map[string]string{
		"If-Match": "*",
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match missing status=%d, want 412", resp.StatusCode)
	}
	resp, body = do(t, http.MethodPut, missingURL, []byte("created"), map[string]string{
		"If-None-Match": "*",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("If-None-Match missing status=%d body=%q", resp.StatusCode, body)
	}
}

func assertS3Body(t *testing.T, objectURL, want string) {
	t.Helper()
	resp, body := do(t, http.MethodGet, objectURL, nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != want {
		t.Fatalf("GET status=%d body=%q, want %q", resp.StatusCode, body, want)
	}
}

func TestS3CurrentVersionHeaders(t *testing.T) {
	s := newTestServer(t)
	plainResponse, _ := do(t, http.MethodPut, s.URL+"/plain/a.txt", []byte("plain"), nil)
	if got := plainResponse.Header.Get("x-amz-version-id"); got != "" {
		t.Fatalf("unversioned PUT exposed version %q", got)
	}
	enableS3Versioning(t, s.URL, "versioned")

	objectURL := s.URL + "/versioned/a.txt"
	putResponse, _ := do(t, http.MethodPut, objectURL, []byte("versioned"), nil)
	versionID := putResponse.Header.Get("x-amz-version-id")
	if versionID == "" {
		t.Fatal("versioned PUT omitted x-amz-version-id")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response, _ := do(t, method, objectURL, nil, nil)
		if got := response.Header.Get("x-amz-version-id"); got != versionID {
			t.Fatalf("%s x-amz-version-id = %q, want %q", method, got, versionID)
		}
	}

	copyResponse, body := do(t, http.MethodPut, s.URL+"/versioned/copy.txt", nil, map[string]string{
		"x-amz-copy-source": "/versioned/a.txt",
	})
	if copyResponse.StatusCode != http.StatusOK {
		t.Fatalf("COPY status=%d body=%s", copyResponse.StatusCode, body)
	}
	if got := copyResponse.Header.Get("x-amz-version-id"); got == "" || got == versionID {
		t.Fatalf("COPY x-amz-version-id = %q, want a new version", got)
	}
}

func TestS3CompleteMultipartVersionHeader(t *testing.T) {
	s := newTestServer(t)
	enableS3Versioning(t, s.URL, "versioned")
	objectURL := s.URL + "/versioned/multipart.txt"
	response, body := do(t, http.MethodPost, objectURL+"?uploads", nil, nil)
	var initiated initiateMultipartUploadResult
	if response.StatusCode != http.StatusOK || xml.Unmarshal(body, &initiated) != nil {
		t.Fatalf("init multipart status=%d body=%s", response.StatusCode, body)
	}
	response, _ = do(t, http.MethodPut,
		objectURL+"?partNumber=1&uploadId="+initiated.UploadID,
		[]byte("multipart"), nil)
	manifest, _ := xml.Marshal(completeMultipartUpload{Parts: []completePartItem{{
		PartNumber: 1, ETag: response.Header.Get("ETag"),
	}}})
	response, body = do(t, http.MethodPost,
		objectURL+"?uploadId="+initiated.UploadID, manifest, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("complete multipart status=%d body=%s", response.StatusCode, body)
	}
	if got := response.Header.Get("x-amz-version-id"); got == "" {
		t.Fatal("CompleteMultipartUpload omitted x-amz-version-id")
	}
}

func enableS3Versioning(t *testing.T, baseURL, bucket string) {
	t.Helper()
	body, _ := xml.Marshal(versioningConfiguration{Status: "Enabled"})
	response, responseBody := do(t, http.MethodPut, baseURL+"/"+bucket+"?versioning", body, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("enable versioning status=%d body=%s", response.StatusCode, responseBody)
	}
}

// S3 GetObject/HeadObject honor ?versionId=<id>, returning that specific stored
// version (and its x-amz-version-id), not just the current one.
func TestS3GetObject_VersionId(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "obj")})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	if err := svc.SetBucketVersioning(ctx, "default", "b", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "default", "b", "k.txt", strings.NewReader("version-one"), 11, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := svc.Put(ctx, "default", "b", "k.txt", strings.NewReader("version-two"), 11, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewRouter(svc, nil, allowAllProvider{}))
	defer srv.Close()

	// Current GET returns v2.
	resp, body := do(t, "GET", srv.URL+"/b/k.txt", nil, nil)
	if resp.StatusCode != 200 || string(body) != "version-two" {
		t.Fatalf("current GET: status=%d body=%q", resp.StatusCode, body)
	}

	// ?versionId returns v1 + the version-id header.
	resp, body = do(t, "GET", srv.URL+"/b/k.txt?versionId="+v1.VersionID, nil, nil)
	if resp.StatusCode != 200 || string(body) != "version-one" {
		t.Fatalf("versionId GET: status=%d body=%q (want version-one)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("x-amz-version-id"); got != v1.VersionID {
		t.Fatalf("x-amz-version-id = %q, want %q", got, v1.VersionID)
	}

	// HEAD ?versionId returns headers for v1.
	resp, _ = do(t, "HEAD", srv.URL+"/b/k.txt?versionId="+v1.VersionID, nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("x-amz-version-id") != v1.VersionID {
		t.Fatalf("HEAD versionId: status=%d vid=%q", resp.StatusCode, resp.Header.Get("x-amz-version-id"))
	}

	// CopySource versionId selects that historical source version.
	resp, body = do(t, "PUT", srv.URL+"/b/copied-v1.txt", nil, map[string]string{
		"x-amz-copy-source": "/b/k.txt?versionId=" + v1.VersionID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("copy historical version status=%d body=%q", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", srv.URL+"/b/copied-v1.txt", nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "version-one" {
		t.Fatalf("copied historical version status=%d body=%q", resp.StatusCode, body)
	}

	// DELETE with versionId removes only that version, not the current object.
	resp, body = do(t, "DELETE", srv.URL+"/b/k.txt?versionId="+v1.VersionID, nil, nil)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("x-amz-version-id") != v1.VersionID {
		t.Fatalf("DELETE versionId: status=%d version=%q body=%q",
			resp.StatusCode, resp.Header.Get("x-amz-version-id"), body)
	}
	resp, _ = do(t, "GET", srv.URL+"/b/k.txt?versionId="+v1.VersionID, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted version GET status = %d, want 404", resp.StatusCode)
	}
	resp, body = do(t, "GET", srv.URL+"/b/k.txt", nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "version-two" {
		t.Fatalf("current version changed after exact delete: status=%d body=%q", resp.StatusCode, body)
	}

	// DELETE without versionId creates a delete marker and preserves v2.
	resp, body = do(t, "DELETE", srv.URL+"/b/k.txt", nil, nil)
	markerID := resp.Header.Get("x-amz-version-id")
	if resp.StatusCode != http.StatusNoContent ||
		resp.Header.Get("x-amz-delete-marker") != "true" || markerID == "" {
		t.Fatalf("delete marker: status=%d marker=%q version=%q body=%q",
			resp.StatusCode, resp.Header.Get("x-amz-delete-marker"), markerID, body)
	}
	resp, _ = do(t, "GET", srv.URL+"/b/k.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET through delete marker status = %d, want 404", resp.StatusCode)
	}
	resp, body = do(t, "GET", srv.URL+"/b/k.txt?versionId="+v2.VersionID, nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "version-two" {
		t.Fatalf("version hidden by marker: status=%d body=%q", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", srv.URL+"/b/?versions", nil, nil)
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "<DeleteMarker>") ||
		!strings.Contains(string(body), markerID) {
		t.Fatalf("version listing omitted delete marker: status=%d body=%s", resp.StatusCode, body)
	}

	// Removing the marker makes the most recent real version current again.
	resp, _ = do(t, "DELETE", srv.URL+"/b/k.txt?versionId="+markerID, nil, nil)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("x-amz-delete-marker") != "true" {
		t.Fatalf("delete marker version status=%d marker=%q",
			resp.StatusCode, resp.Header.Get("x-amz-delete-marker"))
	}
	resp, body = do(t, "GET", srv.URL+"/b/k.txt", nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "version-two" {
		t.Fatalf("current version not restored: status=%d body=%q", resp.StatusCode, body)
	}
}
