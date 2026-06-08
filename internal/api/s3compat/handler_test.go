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
	svc := service.NewFileService(store, repo, nil)
	srv := httptest.NewServer(NewRouter(svc, nil))
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
