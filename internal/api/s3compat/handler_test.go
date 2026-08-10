package s3compat

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "s3.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	srv := httptest.NewServer(NewRouter(svc, nil, allowAllProvider{}))
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv
}

func do(t *testing.T, method, url string, body []byte, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestPutGetHeadCopy(t *testing.T) {
	s := newTestServer(t)
	base := s.URL

	// Put
	resp, _ := do(t, "PUT", base+"/b/hello.txt", []byte("hello world"), map[string]string{"Content-Type": "text/plain"})
	if resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	// Get
	resp, body := do(t, "GET", base+"/b/hello.txt", nil, nil)
	if resp.StatusCode != 200 || string(body) != "hello world" {
		t.Fatalf("get status=%d body=%q", resp.StatusCode, body)
	}
	// Head
	resp, _ = do(t, "HEAD", base+"/b/hello.txt", nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("ETag") == "" {
		t.Fatalf("head status=%d etag=%q", resp.StatusCode, resp.Header.Get("ETag"))
	}

	// Copy hello.txt -> copy.txt
	resp, body = do(t, "PUT", base+"/b/copy.txt", nil, map[string]string{"x-amz-copy-source": "/b/hello.txt"})
	if resp.StatusCode != 200 {
		t.Fatalf("copy status=%d body=%s", resp.StatusCode, body)
	}
	var cr copyObjectResult
	if err := xml.Unmarshal(body, &cr); err != nil || cr.ETag == "" {
		t.Fatalf("copy result parse: %v body=%s", err, body)
	}
	resp, body = do(t, "GET", base+"/b/copy.txt", nil, nil)
	if resp.StatusCode != 200 || string(body) != "hello world" {
		t.Fatalf("copied object: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestObjectTagging(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b/tagged.txt", []byte("x"), nil)

	tagBody, _ := xml.Marshal(tagging{TagSet: []s3Tag{{Key: "team", Value: "research"}, {Key: "env", Value: "prod"}}})
	resp, _ := do(t, "PUT", base+"/b/tagged.txt?tagging", tagBody, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put tagging status %d", resp.StatusCode)
	}
	resp, body := do(t, "GET", base+"/b/tagged.txt?tagging", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get tagging status %d", resp.StatusCode)
	}
	var got tagging
	if err := xml.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse tagging: %v body=%s", err, body)
	}
	m := map[string]string{}
	for _, tg := range got.TagSet {
		m[tg.Key] = tg.Value
	}
	if m["team"] != "research" || m["env"] != "prod" {
		t.Fatalf("tags roundtrip mismatch: %+v", m)
	}
	// Delete tags
	resp, _ = do(t, "DELETE", base+"/b/tagged.txt?tagging", nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete tagging status %d", resp.StatusCode)
	}
	_, body = do(t, "GET", base+"/b/tagged.txt?tagging", nil, nil)
	var after tagging
	_ = xml.Unmarshal(body, &after)
	if len(after.TagSet) != 0 {
		t.Fatalf("expected no tags after delete, got %+v", after.TagSet)
	}
}

func TestMultipartRoundTrip(t *testing.T) {
	s := newTestServer(t)
	base := s.URL

	// Create
	resp, body := do(t, "POST", base+"/b/big.bin?uploads", nil, map[string]string{"Content-Type": "application/octet-stream"})
	if resp.StatusCode != 200 {
		t.Fatalf("create mpu status=%d body=%s", resp.StatusCode, body)
	}
	var init initiateMultipartUploadResult
	if err := xml.Unmarshal(body, &init); err != nil || init.UploadID == "" {
		t.Fatalf("parse init: %v body=%s", err, body)
	}
	uid := init.UploadID

	// ListMultipartUploads shows it
	_, body = do(t, "GET", base+"/b/?uploads", nil, nil)
	var lmu listMultipartUploadsResult
	_ = xml.Unmarshal(body, &lmu)
	found := false
	for _, u := range lmu.Uploads {
		if u.UploadID == uid && u.Key == "big.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("upload not listed: %s", body)
	}

	// Upload two parts
	resp, _ = do(t, "PUT", base+"/b/big.bin?partNumber=1&uploadId="+uid, []byte("AAAA"), nil)
	if resp.StatusCode != 200 || resp.Header.Get("ETag") == "" {
		t.Fatalf("part1 status=%d etag=%q", resp.StatusCode, resp.Header.Get("ETag"))
	}
	do(t, "PUT", base+"/b/big.bin?partNumber=2&uploadId="+uid, []byte("BBBB"), nil)

	// ListParts shows 2
	_, body = do(t, "GET", base+"/b/big.bin?uploadId="+uid, nil, nil)
	var lp listPartsResult
	_ = xml.Unmarshal(body, &lp)
	if len(lp.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %s", len(lp.Parts), body)
	}

	// Complete
	manifest, _ := xml.Marshal(completeMultipartUpload{Parts: []completePartItem{{PartNumber: 1, ETag: lp.Parts[0].ETag}, {PartNumber: 2, ETag: lp.Parts[1].ETag}}})
	resp, body = do(t, "POST", base+"/b/big.bin?uploadId="+uid, manifest, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("complete status=%d body=%s", resp.StatusCode, body)
	}
	var cmr completeMultipartUploadResult
	if err := xml.Unmarshal(body, &cmr); err != nil || cmr.Key != "big.bin" {
		t.Fatalf("parse complete: %v body=%s", err, body)
	}

	// Object now downloadable with concatenated content
	resp, body = do(t, "GET", base+"/b/big.bin", nil, nil)
	if resp.StatusCode != 200 || string(body) != "AAAABBBB" {
		t.Fatalf("assembled object: status=%d body=%q", resp.StatusCode, body)
	}
}

// An out-of-range partNumber (e.g. 0) is rejected, not silently accepted.
func TestMultipart_InvalidPartNumberRejected(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	_, body := do(t, "POST", base+"/b/x.bin?uploads", nil, map[string]string{"Content-Type": "application/octet-stream"})
	var init initiateMultipartUploadResult
	if err := xml.Unmarshal(body, &init); err != nil || init.UploadID == "" {
		t.Fatalf("parse init: %v body=%s", err, body)
	}
	resp, _ := do(t, "PUT", base+"/b/x.bin?partNumber=0&uploadId="+init.UploadID, []byte("AAAA"), nil)
	if resp.StatusCode < 400 {
		t.Fatalf("partNumber=0 should be rejected, got status %d", resp.StatusCode)
	}
}

func TestMultipartAbort(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	_, body := do(t, "POST", base+"/b/ab.bin?uploads", nil, nil)
	var init initiateMultipartUploadResult
	_ = xml.Unmarshal(body, &init)
	do(t, "PUT", base+"/b/ab.bin?partNumber=1&uploadId="+init.UploadID, []byte("x"), nil)
	resp, _ := do(t, "DELETE", base+"/b/ab.bin?uploadId="+init.UploadID, nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("abort status %d", resp.StatusCode)
	}
	// No longer listed
	_, body = do(t, "GET", base+"/b/?uploads", nil, nil)
	if strings.Contains(string(body), init.UploadID) {
		t.Fatalf("aborted upload still listed: %s", body)
	}
}

func TestBatchDelete(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b/d1.txt", []byte("1"), nil)
	do(t, "PUT", base+"/b/d2.txt", []byte("2"), nil)

	req, _ := xml.Marshal(deleteRequest{Objects: []deleteRequestObject{{Key: "d1.txt"}, {Key: "d2.txt"}, {Key: "missing.txt"}}})
	resp, body := do(t, "POST", base+"/b/?delete", req, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("batch delete status=%d body=%s", resp.StatusCode, body)
	}
	var dr deleteResult
	if err := xml.Unmarshal(body, &dr); err != nil {
		t.Fatalf("parse delete result: %v body=%s", err, body)
	}
	if len(dr.Deleted) != 3 {
		t.Fatalf("expected 3 deleted entries (missing is idempotent), got %d: %s", len(dr.Deleted), body)
	}
	// Both gone
	if resp, _ := do(t, "GET", base+"/b/d1.txt", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("d1 should be gone, got %d", resp.StatusCode)
	}
}

func TestDeleteBucketRequiresEmptyBucket(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	resp, _ := do(t, "PUT", base+"/bucket-delete", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create bucket status = %d", resp.StatusCode)
	}
	do(t, "PUT", base+"/bucket-delete/object.txt", []byte("x"), nil)
	resp, body := do(t, "DELETE", base+"/bucket-delete", nil, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "BucketNotEmpty") {
		t.Fatalf("non-empty bucket delete status=%d body=%s", resp.StatusCode, body)
	}
	do(t, "DELETE", base+"/bucket-delete/object.txt", nil, nil)
	resp, body = do(t, "DELETE", base+"/bucket-delete", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("empty bucket delete status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = do(t, "DELETE", base+"/bucket-delete", nil, nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "NoSuchBucket") {
		t.Fatalf("missing bucket delete status=%d body=%s", resp.StatusCode, body)
	}
}

func TestObjectACL(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	// PUT with canned ACL header.
	do(t, "PUT", base+"/b/pub.txt", []byte("data"), map[string]string{"x-amz-acl": "public-read"})

	resp, body := do(t, "GET", base+"/b/pub.txt?acl", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get acl status=%d", resp.StatusCode)
	}
	var p accessControlPolicy
	if err := xml.Unmarshal(body, &p); err != nil {
		t.Fatalf("parse acl: %v body=%s", err, body)
	}
	hasAllUsersRead := false
	for _, g := range p.Grants {
		if g.Grantee.URI == allUsersURI && g.Permission == "READ" {
			hasAllUsersRead = true
		}
	}
	if !hasAllUsersRead {
		t.Fatalf("expected AllUsers READ grant, got %+v", p.Grants)
	}

	// Flip back to private via PUT ?acl.
	resp, _ = do(t, "PUT", base+"/b/pub.txt?acl", nil, map[string]string{"x-amz-acl": "private"})
	if resp.StatusCode != 200 {
		t.Fatalf("put acl status=%d", resp.StatusCode)
	}
	_, body = do(t, "GET", base+"/b/pub.txt?acl", nil, nil)
	var p2 accessControlPolicy
	_ = xml.Unmarshal(body, &p2)
	for _, g := range p2.Grants {
		if g.Grantee.URI == allUsersURI {
			t.Fatalf("expected no public grant after private, got %+v", p2.Grants)
		}
	}

	// AccessControlPolicy XML bodies are accepted without a canned header.
	policyBody, _ := xml.Marshal(cannedToPolicy("public-read"))
	resp, body = do(t, "PUT", base+"/b/pub.txt?acl", policyBody, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put XML acl status=%d body=%s", resp.StatusCode, body)
	}
	_, body = do(t, "GET", base+"/b/pub.txt?acl", nil, nil)
	if !strings.Contains(string(body), allUsersURI) {
		t.Fatalf("XML ACL was not persisted: %s", body)
	}
}

func TestS3ContentMD5RoundTrip(t *testing.T) {
	s := newTestServer(t)
	const md5b64 = "XrY7u+Ae7tCTyyK7j1rNww==" // "hello world" MD5 base64
	base := s.URL + "/bucket/md5test.txt"

	// PUT with Content-MD5.
	resp, _ := do(t, "PUT", base, []byte("hello world"), map[string]string{
		"Content-MD5": md5b64,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("PUT: status=%d want 200", resp.StatusCode)
	}

	// GET should echo x-amz-checksum-md5.
	resp, body := do(t, "GET", base, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET: status=%d want 200", resp.StatusCode)
	}
	if resp.Header.Get("x-amz-checksum-md5") != md5b64 {
		t.Errorf("GET: x-amz-checksum-md5=%q want %q", resp.Header.Get("x-amz-checksum-md5"), md5b64)
	}
	if string(body) != "hello world" {
		t.Errorf("GET: body=%q want %q", body, "hello world")
	}

	// HEAD should also echo x-amz-checksum-md5.
	resp, body = do(t, "HEAD", base, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("HEAD: status=%d want 200", resp.StatusCode)
	}
	if resp.Header.Get("x-amz-checksum-md5") != md5b64 {
		t.Errorf("HEAD: x-amz-checksum-md5=%q want %q", resp.Header.Get("x-amz-checksum-md5"), md5b64)
	}
	if len(body) != 0 {
		t.Errorf("HEAD: body should be empty, got %q", body)
	}

	// PUT without Content-MD5 header.
	resp, _ = do(t, "PUT", s.URL+"/bucket/nomd5.txt", []byte("data"), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT no-md5: status=%d want 200", resp.StatusCode)
	}
	resp, body = do(t, "GET", s.URL+"/bucket/nomd5.txt", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET no-md5: status=%d want 200", resp.StatusCode)
	}
	if resp.Header.Get("x-amz-checksum-md5") != "" {
		t.Errorf("GET without MD5: x-amz-checksum-md5 should be empty, got %q", resp.Header.Get("x-amz-checksum-md5"))
	}

	// System _aero_ prefix should not leak into x-amz-meta-* headers.
	if v := resp.Header.Get("x-amz-meta-_aero-content-md5"); v != "" {
		t.Errorf("system metadata should not be leaked: x-amz-meta-_aero-content-md5=%q", v)
	}
}

func TestS3ContentMD5MismatchReturnsBadDigest(t *testing.T) {
	s := newTestServer(t)
	resp, body := do(
		t, http.MethodPut, s.URL+"/bucket/bad-md5.txt", []byte("content"),
		map[string]string{"Content-MD5": "AAAAAAAAAAAAAAAAAAAAAA=="},
	)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT status=%d, want 400; body=%s", resp.StatusCode, body)
	}
	var got s3Error
	if err := xml.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "BadDigest" {
		t.Fatalf("error code=%q, want BadDigest", got.Code)
	}
}

func TestS3ObjectMetaHidesInternalFields(t *testing.T) {
	w := httptest.NewRecorder()
	writeS3ObjectMeta(w, map[string]string{
		"author":       "Ada",
		"_aero_owner":  "subject-1",
		"_AeRo_secret": "hidden",
	})

	if got := w.Header().Get("x-amz-meta-author"); got != "Ada" {
		t.Fatalf("x-amz-meta-author = %q, want Ada", got)
	}
	for _, key := range []string{"x-amz-meta-_aero_owner", "x-amz-meta-_AeRo_secret"} {
		if got := w.Header().Get(key); got != "" {
			t.Errorf("internal metadata leaked through %s: %q", key, got)
		}
	}
}

func TestS3MetadataLimitErrorsAreInvalidArguments(t *testing.T) {
	for _, err := range []error{
		service.ErrMetadataTooLarge,
		service.ErrMetadataKeyTooLong,
		service.ErrMetadataValueTooLong,
	} {
		code, _, status := classify(err)
		if status != http.StatusBadRequest || code != "InvalidArgument" {
			t.Fatalf("classify(%v)=(%q,%d), want InvalidArgument,400", err, code, status)
		}
	}
}

func TestRangeRequests(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	// 26-byte alphabet
	do(t, "PUT", base+"/b/alpha.txt", []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ"), nil)

	// bytes=5-9 → "FGHIJ"
	resp, body := do(t, "GET", base+"/b/alpha.txt", nil, map[string]string{"Range": "bytes=5-9"})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status=%d want 206", resp.StatusCode)
	}
	if string(body) != "FGHIJ" {
		t.Fatalf("range body=%q want FGHIJ", body)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 5-9/26" {
		t.Fatalf("content-range=%q want bytes 5-9/26", cr)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "5" {
		t.Fatalf("content-length=%q want 5", cl)
	}

	// suffix bytes=-3 → "XYZ"
	_, body = do(t, "GET", base+"/b/alpha.txt", nil, map[string]string{"Range": "bytes=-3"})
	if string(body) != "XYZ" {
		t.Fatalf("suffix range body=%q want XYZ", body)
	}

	// open-ended bytes=23- → "XYZ"
	_, body = do(t, "GET", base+"/b/alpha.txt", nil, map[string]string{"Range": "bytes=23-"})
	if string(body) != "XYZ" {
		t.Fatalf("open range body=%q want XYZ", body)
	}

	// unsatisfiable → 416
	resp, _ = do(t, "GET", base+"/b/alpha.txt", nil, map[string]string{"Range": "bytes=100-200"})
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable status=%d want 416", resp.StatusCode)
	}

	// full GET still 200
	resp, body = do(t, "GET", base+"/b/alpha.txt", nil, nil)
	if resp.StatusCode != 200 || len(body) != 26 {
		t.Fatalf("full get status=%d len=%d", resp.StatusCode, len(body))
	}
}

func TestBucketVersioningRoundTrip(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)

	// Unconfigured: empty VersioningConfiguration, no Status.
	resp, body := do(t, "GET", base+"/b?versioning", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get versioning status %d", resp.StatusCode)
	}
	var vc versioningConfiguration
	if err := xml.Unmarshal(body, &vc); err != nil {
		t.Fatalf("parse versioning: %v body=%s", err, body)
	}
	if vc.Status != "" {
		t.Fatalf("expected empty Status when unconfigured, got %q", vc.Status)
	}

	// Enable.
	putBody, _ := xml.Marshal(versioningConfiguration{Status: "Enabled"})
	resp, _ = do(t, "PUT", base+"/b?versioning", putBody, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put versioning status %d", resp.StatusCode)
	}
	resp, body = do(t, "GET", base+"/b?versioning", nil, nil)
	_ = xml.Unmarshal(body, &vc)
	if resp.StatusCode != 200 || vc.Status != "Enabled" {
		t.Fatalf("expected Enabled, got status=%d Status=%q body=%s", resp.StatusCode, vc.Status, body)
	}
}

func TestBucketLifecycleRoundTrip(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)

	// Unconfigured → 404 NoSuchLifecycleConfiguration.
	resp, body := do(t, "GET", base+"/b?lifecycle", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("unconfigured lifecycle GET status=%d want 404 body=%s", resp.StatusCode, body)
	}
	var e s3Error
	_ = xml.Unmarshal(body, &e)
	if e.Code != "NoSuchLifecycleConfiguration" {
		t.Fatalf("expected NoSuchLifecycleConfiguration, got %q body=%s", e.Code, body)
	}

	// Configure Days=30.
	putBody, _ := xml.Marshal(lifecycleConfiguration{Rules: []lifecycleRule{{Status: "Enabled", Expiration: &lifecycleExpiration{Days: 30}}}})
	resp, _ = do(t, "PUT", base+"/b?lifecycle", putBody, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put lifecycle status %d", resp.StatusCode)
	}
	resp, body = do(t, "GET", base+"/b?lifecycle", nil, nil)
	var lc lifecycleConfiguration
	if err := xml.Unmarshal(body, &lc); err != nil {
		t.Fatalf("parse lifecycle: %v body=%s", err, body)
	}
	if resp.StatusCode != 200 || len(lc.Rules) != 1 || lc.Rules[0].Expiration == nil || lc.Rules[0].Expiration.Days != 30 {
		t.Fatalf("expected one rule with Days=30, got status=%d %+v body=%s", resp.StatusCode, lc.Rules, body)
	}
}

func TestBucketObjectLockRoundTrip(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)

	putBody, _ := xml.Marshal(objectLockConfiguration{
		ObjectLockEnabled: "Enabled",
		Rule:              &objectLockRule{DefaultRetention: objectLockRetention{Mode: "GOVERNANCE", Days: 1}},
	})
	resp, _ := do(t, "PUT", base+"/b?object-lock", putBody, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put object-lock status %d", resp.StatusCode)
	}
	resp, body := do(t, "GET", base+"/b?object-lock", nil, nil)
	var olc objectLockConfiguration
	if err := xml.Unmarshal(body, &olc); err != nil {
		t.Fatalf("parse object-lock: %v body=%s", err, body)
	}
	if resp.StatusCode != 200 || olc.ObjectLockEnabled != "Enabled" || olc.Rule == nil {
		t.Fatalf("expected enabled lock with a rule, got status=%d %+v body=%s", resp.StatusCode, olc, body)
	}
	if olc.Rule.DefaultRetention.Days != 1 || olc.Rule.DefaultRetention.Mode != "GOVERNANCE" {
		t.Fatalf("expected GOVERNANCE Days=1, got %+v", olc.Rule.DefaultRetention)
	}
}

func TestListObjectVersions(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)

	// Enable versioning, then write the same key twice.
	putBody, _ := xml.Marshal(versioningConfiguration{Status: "Enabled"})
	do(t, "PUT", base+"/b?versioning", putBody, nil)
	do(t, "PUT", base+"/b/k.txt", []byte("v1"), nil)
	do(t, "PUT", base+"/b/k.txt", []byte("v2"), nil)

	resp, body := do(t, "GET", base+"/b?versions", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list versions status=%d body=%s", resp.StatusCode, body)
	}
	var lvr listVersionsResult
	if err := xml.Unmarshal(body, &lvr); err != nil {
		t.Fatalf("parse versions: %v body=%s", err, body)
	}
	if lvr.Name != "b" {
		t.Fatalf("expected Name=b, got %q", lvr.Name)
	}
	if len(lvr.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d: %s", len(lvr.Versions), body)
	}
	latest := 0
	for _, v := range lvr.Versions {
		if v.Key != "k.txt" {
			t.Fatalf("unexpected version key %q", v.Key)
		}
		if v.IsLatest {
			latest++
		}
	}
	if latest != 1 {
		t.Fatalf("expected exactly one IsLatest=true, got %d: %s", latest, body)
	}
}

func TestBucketConfigMalformedXML(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)

	resp, body := do(t, "PUT", base+"/b?versioning", []byte("<not-xml"), nil)
	if resp.StatusCode != 400 {
		t.Fatalf("malformed PUT status=%d want 400 body=%s", resp.StatusCode, body)
	}
	var e s3Error
	_ = xml.Unmarshal(body, &e)
	if e.Code != "MalformedXML" {
		t.Fatalf("expected MalformedXML, got %q body=%s", e.Code, body)
	}
}

func TestBucketACLRoundTrip(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)

	// Default (private): owner FULL_CONTROL grant, no AllUsers grant.
	resp, body := do(t, "GET", base+"/b?acl", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get acl status=%d body=%s", resp.StatusCode, body)
	}
	var pol accessControlPolicy
	if err := xml.Unmarshal(body, &pol); err != nil {
		t.Fatalf("parse acl: %v body=%s", err, body)
	}
	if pol.Owner.ID == "" || len(pol.Grants) != 1 || pol.Grants[0].Permission != "FULL_CONTROL" {
		t.Fatalf("default acl not private/owner-only: %s", body)
	}
	for _, g := range pol.Grants {
		if g.Grantee.URI == allUsersURI {
			t.Fatalf("private bucket should have no AllUsers grant: %s", body)
		}
	}

	// PUT canned public-read via x-amz-acl header, then confirm the AllUsers READ grant.
	resp, _ = do(t, "PUT", base+"/b?acl", nil, map[string]string{"x-amz-acl": "public-read"})
	if resp.StatusCode != 200 {
		t.Fatalf("put acl status=%d", resp.StatusCode)
	}
	resp, body = do(t, "GET", base+"/b?acl", nil, nil)
	pol = accessControlPolicy{}
	_ = xml.Unmarshal(body, &pol)
	gotAllUsersRead := false
	for _, g := range pol.Grants {
		if g.Grantee.URI == allUsersURI && g.Permission == "READ" {
			gotAllUsersRead = true
		}
	}
	if resp.StatusCode != 200 || !gotAllUsersRead {
		t.Fatalf("expected AllUsers READ grant after public-read, got status=%d body=%s", resp.StatusCode, body)
	}

	// PUT via an AccessControlPolicy body: AllUsers FULL_CONTROL must map to
	// public-read-write (FULL_CONTROL implies write), so a later GET shows a
	// WRITE grant to AllUsers.
	aclBody, _ := xml.Marshal(accessControlPolicy{
		Owner:  aclOwner{ID: "aero-vault"},
		Grants: []aclGrant{{Grantee: aclGrantee{Type: "Group", URI: allUsersURI}, Permission: "FULL_CONTROL"}},
	})
	if resp, _ := do(t, "PUT", base+"/b?acl", aclBody, nil); resp.StatusCode != 200 {
		t.Fatalf("put acl body status=%d", resp.StatusCode)
	}
	resp, body = do(t, "GET", base+"/b?acl", nil, nil)
	pol = accessControlPolicy{}
	_ = xml.Unmarshal(body, &pol)
	gotAllUsersWrite := false
	for _, g := range pol.Grants {
		if g.Grantee.URI == allUsersURI && g.Permission == "WRITE" {
			gotAllUsersWrite = true
		}
	}
	if resp.StatusCode != 200 || !gotAllUsersWrite {
		t.Fatalf("expected AllUsers WRITE grant after FULL_CONTROL body (public-read-write), got status=%d body=%s", resp.StatusCode, body)
	}
}

func TestBucketLocation(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)

	resp, body := do(t, "GET", base+"/b?location", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get location status=%d body=%s", resp.StatusCode, body)
	}
	var lc locationConstraint
	if err := xml.Unmarshal(body, &lc); err != nil {
		t.Fatalf("parse location: %v body=%s", err, body)
	}

	// Missing bucket → 404 NoSuchBucket.
	resp, body = do(t, "GET", base+"/missing?location", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("location on missing bucket status=%d want 404 body=%s", resp.StatusCode, body)
	}
	var e s3Error
	_ = xml.Unmarshal(body, &e)
	if e.Code != "NoSuchBucket" {
		t.Fatalf("expected NoSuchBucket, got %q body=%s", e.Code, body)
	}
}

func TestBucketLifecycleDelete(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)

	// Configure, confirm present.
	putBody, _ := xml.Marshal(lifecycleConfiguration{Rules: []lifecycleRule{{Status: "Enabled", Expiration: &lifecycleExpiration{Days: 30}}}})
	if resp, _ := do(t, "PUT", base+"/b?lifecycle", putBody, nil); resp.StatusCode != 200 {
		t.Fatalf("put lifecycle status=%d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/b?lifecycle", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("get lifecycle after put status=%d want 200", resp.StatusCode)
	}

	// Delete → 204, then GET → 404 NoSuchLifecycleConfiguration.
	resp, _ := do(t, "DELETE", base+"/b?lifecycle", nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete lifecycle status=%d want 204", resp.StatusCode)
	}
	resp, body := do(t, "GET", base+"/b?lifecycle", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("get lifecycle after delete status=%d want 404 body=%s", resp.StatusCode, body)
	}
	var e s3Error
	_ = xml.Unmarshal(body, &e)
	if e.Code != "NoSuchLifecycleConfiguration" {
		t.Fatalf("expected NoSuchLifecycleConfiguration, got %q body=%s", e.Code, body)
	}
}

func TestListObjectVersionsPagination(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b", nil, nil)
	// Versioning on, and one key gets two versions, so the page also exercises
	// multiple <Version> rows per key (not just one row per key).
	putBody, _ := xml.Marshal(versioningConfiguration{Status: "Enabled"})
	do(t, "PUT", base+"/b?versioning", putBody, nil)
	for _, k := range []string{"a.txt", "b.txt", "c.txt"} {
		do(t, "PUT", base+"/b/"+k, []byte("x"), nil)
	}
	do(t, "PUT", base+"/b/a.txt", []byte("x2"), nil) // a.txt now has 2 versions

	// max-keys bounds the combined number of versions and delete markers, not
	// the number of distinct object keys.
	resp, body := do(t, "GET", base+"/b?versions&max-keys=2", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("versions page1 status=%d body=%s", resp.StatusCode, body)
	}
	var p1 listVersionsResult
	if err := xml.Unmarshal(body, &p1); err != nil {
		t.Fatalf("parse page1: %v body=%s", err, body)
	}
	if !p1.IsTruncated || p1.NextKeyMarker == "" || p1.NextVersionIdMarker == "" {
		t.Fatalf("expected version continuation on page1, got truncated=%v key=%q version=%q body=%s",
			p1.IsTruncated, p1.NextKeyMarker, p1.NextVersionIdMarker, body)
	}
	if len(p1.Versions)+len(p1.DeleteMarkers) != 2 {
		t.Fatalf("page1 entries=%d, want max-keys=2: %s", len(p1.Versions)+len(p1.DeleteMarkers), body)
	}
	aLatest := 0
	for _, v := range p1.Versions {
		if v.Key == "a.txt" && v.IsLatest {
			aLatest++
		}
	}
	if aLatest != 1 {
		t.Fatalf("expected exactly one IsLatest among a.txt's versions, got %d: %s", aLatest, body)
	}

	// Resume with both markers. The final page contains b.txt and c.txt and
	// must not repeat either a.txt version.
	resp, body = do(t, "GET", base+"/b?versions&max-keys=2&key-marker="+
		p1.NextKeyMarker+"&version-id-marker="+p1.NextVersionIdMarker, nil, nil)
	var p2 listVersionsResult
	_ = xml.Unmarshal(body, &p2)
	if resp.StatusCode != 200 || p2.IsTruncated || len(p2.Versions) != 2 {
		t.Fatalf("expected final page with 2 versions, got status=%d truncated=%v n=%d body=%s", resp.StatusCode, p2.IsTruncated, len(p2.Versions), body)
	}
	for _, version := range p2.Versions {
		if version.Key == "a.txt" {
			t.Fatalf("continued page repeated completed key a.txt: %s", body)
		}
	}
}

func TestListObjectsV2(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b/a/1.txt", []byte("1"), nil)
	do(t, "PUT", base+"/b/a/2.txt", []byte("2"), nil)
	resp, body := do(t, "GET", base+"/b/?list-type=2&prefix=a/", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list status %d", resp.StatusCode)
	}
	var lbr listBucketResult
	if err := xml.Unmarshal(body, &lbr); err != nil {
		t.Fatalf("parse list: %v body=%s", err, body)
	}
	if lbr.KeyCount != 2 {
		t.Fatalf("expected 2 keys, got %d: %s", lbr.KeyCount, body)
	}
}

func TestListObjectsV1(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	do(t, "PUT", base+"/b/a/1.txt", []byte("11"), map[string]string{"Content-Type": "text/plain"})
	do(t, "PUT", base+"/b/a/2.txt", []byte("222"), map[string]string{"Content-Type": "text/plain"})

	resp, body := do(t, "GET", base+"/b?prefix=a/", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list status %d body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/xml" {
		t.Fatalf("content-type=%q want application/xml", ct)
	}
	if strings.Contains(string(body), "KeyCount") ||
		strings.Contains(string(body), "ContinuationToken") {
		t.Fatalf("v1 response must not carry v2-only fields: %s", body)
	}
	var lbr listBucketResultV1
	if err := xml.Unmarshal(body, &lbr); err != nil {
		t.Fatalf("parse list: %v body=%s", err, body)
	}
	if len(lbr.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d: %s", len(lbr.Contents), body)
	}
	bySize := map[string]int64{}
	for _, c := range lbr.Contents {
		if c.Key == "" || c.ETag == "" {
			t.Fatalf("content missing key/etag: %+v", c)
		}
		bySize[c.Key] = c.Size
	}
	if bySize["a/1.txt"] != 2 || bySize["a/2.txt"] != 3 {
		t.Fatalf("unexpected sizes: %+v", bySize)
	}
}

func TestListObjectsV1Pagination(t *testing.T) {
	s := newTestServer(t)
	base := s.URL
	for _, k := range []string{"p/1.txt", "p/2.txt", "p/3.txt"} {
		do(t, "PUT", base+"/b/"+k, []byte("x"), nil)
	}

	// First page: max-keys smaller than count → truncated with a NextMarker.
	resp, body := do(t, "GET", base+"/b?prefix=p/&max-keys=2", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("page1 status %d body=%s", resp.StatusCode, body)
	}
	var p1 listBucketResultV1
	if err := xml.Unmarshal(body, &p1); err != nil {
		t.Fatalf("parse page1: %v body=%s", err, body)
	}
	if !p1.IsTruncated {
		t.Fatalf("page1 should be truncated: %s", body)
	}
	if p1.NextMarker == "" {
		t.Fatalf("page1 should have NextMarker: %s", body)
	}
	if len(p1.Contents) != 2 {
		t.Fatalf("page1 expected 2 contents, got %d: %s", len(p1.Contents), body)
	}

	// Second page: passing the marker returns the rest.
	resp, body = do(t, "GET", base+"/b?prefix=p/&max-keys=2&marker="+p1.NextMarker, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("page2 status %d body=%s", resp.StatusCode, body)
	}
	var p2 listBucketResultV1
	if err := xml.Unmarshal(body, &p2); err != nil {
		t.Fatalf("parse page2: %v body=%s", err, body)
	}
	if p2.Marker != p1.NextMarker {
		t.Fatalf("page2 Marker=%q want %q", p2.Marker, p1.NextMarker)
	}
	if p2.IsTruncated {
		t.Fatalf("page2 should not be truncated: %s", body)
	}
	if len(p2.Contents) != 1 {
		t.Fatalf("page2 expected 1 content, got %d: %s", len(p2.Contents), body)
	}
}

func TestBucketPolicyEnforcement(t *testing.T) {
	s := newTestServer(t)
	base := s.URL + "/bucket"

	// Create bucket by PUTting an object first.
	resp, _ := do(t, "PUT", base+"/init.txt", []byte("init"), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("init: status=%d", resp.StatusCode)
	}

	// Set a policy: allow all, but deny GetObject from 127.0.0.1 (our test client).
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:*"},{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Condition":{"IpAddress":{"aws:SourceIp":["127.0.0.1/32"]}}}]}`
	resp, _ = do(t, "PUT", base+"/?policy", []byte(policy), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("PUT policy: status=%d want 204", resp.StatusCode)
	}

	// GET should be denied (403) because our source IP matches the Deny.
	resp, _ = do(t, "GET", base+"/init.txt", nil, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("GET after Deny policy: status=%d want 403", resp.StatusCode)
	}

	// PUT should still work (only GetObject was denied).
	resp, _ = do(t, "PUT", base+"/new.txt", []byte("new"), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT after Deny policy: status=%d want 200", resp.StatusCode)
	}

	// Clear the policy.
	resp, _ = do(t, "DELETE", base+"/?policy", nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("DELETE policy: status=%d want 204", resp.StatusCode)
	}

	// GET should work again.
	resp, _ = do(t, "GET", base+"/init.txt", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET after policy cleared: status=%d want 200", resp.StatusCode)
	}
}
