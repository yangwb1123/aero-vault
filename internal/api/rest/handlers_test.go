package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"log/slog"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// bodyPolicy is a helper that wraps a policy JSON string for use as the body
// of a PUT /buckets/{bucket}/policy request: {"policy":"<escaped>"}.
func bodyPolicy(policyJSON string) []byte {
	b, _ := json.Marshal(map[string]string{"policy": policyJSON})
	return b
}

func setupTest(t *testing.T) (*service.FileService, repository.Repository, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("new local storage: %v", err)
	}
	svc := service.NewFileService(store, repo, nil)
	router := NewRouter(svc, repo, nil, nil, nil, nil, nil, slog.Default(), false, nil, 0, false)
	ts := httptest.NewServer(router)
	t.Cleanup(func() { ts.Close(); _ = repo.Close() })
	return svc, repo, ts
}

func TestPutGetDelete(t *testing.T) {
	_, _, ts := setupTest(t)
	base := ts.URL + "/files/myfile.txt"

	resp, body := req(t, "PUT", base, []byte("hello world"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d want 201, body=%s", resp.StatusCode, body)
	}
	var obj objectDTO
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("PUT: decode body: %v", err)
	}
	if obj.Key != "myfile.txt" {
		t.Errorf("PUT: key=%q want myfile.txt", obj.Key)
	}
	if obj.Bucket != "default" {
		t.Errorf("PUT: bucket=%q want default", obj.Bucket)
	}
	if obj.Size != 11 {
		t.Errorf("PUT: size=%d want 11", obj.Size)
	}
	if obj.ETag == "" {
		t.Error("PUT: etag is empty")
	}

	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status=%d want 200", resp.StatusCode)
	}
	if string(body) != "hello world" {
		t.Errorf("GET: body=%q want hello world", body)
	}
	if resp.Header.Get("Content-Type") == "" {
		t.Error("GET: missing Content-Type")
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("GET: missing ETag")
	}
	if resp.Header.Get("Content-Length") != "11" {
		t.Errorf("GET: Content-Length=%q want 11", resp.Header.Get("Content-Length"))
	}

	resp, body = req(t, "HEAD", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: status=%d want 200", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("HEAD: body should be empty, got %q", body)
	}
	if resp.Header.Get("Content-Length") != "11" {
		t.Errorf("HEAD: Content-Length=%q want 11", resp.Header.Get("Content-Length"))
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("HEAD: missing ETag")
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("HEAD: Accept-Ranges=%q want bytes", resp.Header.Get("Accept-Ranges"))
	}

	resp, body = req(t, "DELETE", base, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: status=%d want 204, body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE: status=%d want 404, body=%s", resp.StatusCode, body)
	}
}

func TestContentMD5RoundTrip(t *testing.T) {
	_, _, ts := setupTest(t)
	const md5b64 = "XrY7u+Ae7tCTyyK7j1rNww==" // echo -n "hello world" | md5 | base64 -d | base64
	base := ts.URL + "/files/md5test.txt"

	// PUT with Content-MD5.
	resp, body := req(t, "PUT", base, []byte("hello world"), map[string]string{
		"Content-MD5": md5b64,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT with Content-MD5: status=%d want 201, body=%s", resp.StatusCode, body)
	}

	// GET should echo X-Content-MD5.
	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after MD5 PUT: status=%d want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Content-MD5") != md5b64 {
		t.Errorf("GET: X-Content-MD5=%q want %q", resp.Header.Get("X-Content-MD5"), md5b64)
	}
	if string(body) != "hello world" {
		t.Errorf("GET: body=%q want %q", body, "hello world")
	}

	// HEAD should also echo X-Content-MD5.
	resp, body = req(t, "HEAD", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD after MD5 PUT: status=%d want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Content-MD5") != md5b64 {
		t.Errorf("HEAD: X-Content-MD5=%q want %q", resp.Header.Get("X-Content-MD5"), md5b64)
	}
	if len(body) != 0 {
		t.Errorf("HEAD: body should be empty, got %q", body)
	}

	// PUT WITHOUT Content-MD5 — X-Content-MD5 should be absent in response.
	resp, body = req(t, "PUT", ts.URL+"/files/nomd5.txt", []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT without MD5: status=%d want 201", resp.StatusCode)
	}
	resp, body = req(t, "GET", ts.URL+"/files/nomd5.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET no-MD5: status=%d want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Content-MD5") != "" {
		t.Errorf("GET without MD5: X-Content-MD5 should be empty, got %q", resp.Header.Get("X-Content-MD5"))
	}

	// System _aero_ prefix should not leak into X-Meta-* headers.
	if v := resp.Header.Get("X-Meta-_aero-content-md5"); v != "" {
		t.Errorf("system metadata should not be leaked: X-Meta-_aero-content-md5=%q", v)
	}
}

func TestListPagination(t *testing.T) {
	_, _, ts := setupTest(t)

	req(t, "PUT", ts.URL+"/files/alpha.txt", []byte("a"), nil)
	req(t, "PUT", ts.URL+"/files/beta.txt", []byte("b"), nil)

	resp, body := req(t, "GET", ts.URL+"/files", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	var result listResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("LIST: decode: %v", err)
	}
	if len(result.Objects) != 2 {
		t.Errorf("LIST: count=%d want 2", len(result.Objects))
	}
	if result.HasMore {
		t.Error("LIST: HasMore should be false")
	}

	resp, body = req(t, "GET", ts.URL+"/files?limit=1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST limit=1: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	var page listResponse
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("LIST limit=1: decode: %v", err)
	}
	if len(page.Objects) != 1 {
		t.Errorf("LIST limit=1: count=%d want 1", len(page.Objects))
	}
	if !page.HasMore {
		t.Error("LIST limit=1: HasMore should be true")
	}
	if page.NextMarker == "" {
		t.Error("LIST limit=1: NextMarker should be non-empty")
	}

	resp, body = req(t, "GET", ts.URL+"/files?limit=1&marker="+page.NextMarker, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST page2: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	var page2 listResponse
	if err := json.Unmarshal(body, &page2); err != nil {
		t.Fatalf("LIST page2: decode: %v", err)
	}
	if len(page2.Objects) != 1 {
		t.Errorf("LIST page2: count=%d want 1", len(page2.Objects))
	}

	req(t, "PUT", ts.URL+"/files/other.txt", []byte("c"), nil)
	resp, body = req(t, "GET", ts.URL+"/files?prefix=alpha", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST prefix=alpha: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	var pref listResponse
	if err := json.Unmarshal(body, &pref); err != nil {
		t.Fatalf("LIST prefix=alpha: decode: %v", err)
	}
	if len(pref.Objects) != 1 || pref.Objects[0].Key != "alpha.txt" {
		t.Errorf("LIST prefix=alpha: got %d objects, want alpha.txt only", len(pref.Objects))
	}
}

func TestPutTagsOnUpload(t *testing.T) {
	_, _, ts := setupTest(t)
	base := ts.URL + "/files/tagged.txt"

	resp, _ := req(t, "PUT", base, []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d", resp.StatusCode)
	}

	tagsURL := base + "/tags"
	resp, body := req(t, "PUT", tagsURL, []byte(`{"color":"red","env":"test"}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT tags: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "GET", tagsURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tags: status=%d want 200", resp.StatusCode)
	}
	var obj objectDTO
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("GET tags: decode: %v", err)
	}
	if obj.Tags["color"] != "red" || obj.Tags["env"] != "test" {
		t.Errorf("GET tags: got tags=%v want color=red,env=test", obj.Tags)
	}
}

func TestSearchReturns503WhenDisabled(t *testing.T) {
	_, _, ts := setupTest(t)
	resp, body := req(t, "POST", ts.URL+"/search", []byte(`{"query":"test"}`), nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("search disabled: status=%d want 503, body=%s", resp.StatusCode, body)
	}
}

func TestChatReturns503WhenDisabled(t *testing.T) {
	_, _, ts := setupTest(t)
	resp, body := req(t, "POST", ts.URL+"/chat", []byte(`{"query":"hello"}`), nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("chat disabled: status=%d want 503, body=%s", resp.StatusCode, body)
	}
}

func TestAgentReturns503WhenDisabled(t *testing.T) {
	_, _, ts := setupTest(t)
	resp, body := req(t, "POST", ts.URL+"/agent", []byte(`{"query":"hello"}`), nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("agent disabled: status=%d want 503, body=%s", resp.StatusCode, body)
	}
}

func TestChatStreamReturns503WhenDisabled(t *testing.T) {
	_, _, ts := setupTest(t)
	resp, body := req(t, "POST", ts.URL+"/chat/stream", []byte(`{"query":"hello"}`), nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("chat stream disabled: status=%d want 503, body=%s", resp.StatusCode, body)
	}
}

func TestNonExistentObjectReturns404(t *testing.T) {
	_, _, ts := setupTest(t)
	base := ts.URL + "/files/nope.txt"

	resp, body := req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET missing: status=%d want 404, body=%s", resp.StatusCode, body)
	}
	var errResp errorBody
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("GET missing: decode error: %v", err)
	}
	if errResp.Error.Code != "NotFound" {
		t.Errorf("GET missing: code=%s want NotFound", errResp.Error.Code)
	}

	resp, body = req(t, "HEAD", base, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD missing: status=%d want 404, body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "DELETE", base, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE missing: status=%d want 404, body=%s", resp.StatusCode, body)
	}
}

func TestInvalidKeyReturns400(t *testing.T) {
	_, _, ts := setupTest(t)
	tests := []struct {
		name string
		url  string
	}{
		{"empty key", ts.URL + "/files/"},
		{"dotdot key", ts.URL + "/files/../evil.txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := req(t, "PUT", tc.url, []byte("data"), nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d want 400, body=%s", resp.StatusCode, body)
			}
			var errResp errorBody
			if err := json.Unmarshal(body, &errResp); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if errResp.Error.Code != "InvalidArgument" {
				t.Errorf("code=%s want InvalidArgument", errResp.Error.Code)
			}
		})
	}
}

func TestLockViaRouter(t *testing.T) {
	_, _, ts := setupTest(t)
	base := ts.URL + "/files/lockable.txt"

	resp, _ := req(t, "PUT", base, []byte("lock me"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d", resp.StatusCode)
	}

	resp, body := req(t, "POST", base+"/lock", []byte(`{"seconds":60}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lock: status=%d want 200, body=%s", resp.StatusCode, body)
	}
}

func TestPostKeyInvalidSubresource(t *testing.T) {
	_, _, ts := setupTest(t)
	base := ts.URL + "/files/myfile.txt"

	resp, _ := req(t, "PUT", base, []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d", resp.StatusCode)
	}

	resp, body := req(t, "POST", base+"/bogus", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST bogus subresource: status=%d want 400, body=%s", resp.StatusCode, body)
	}
}

func TestListEmpty(t *testing.T) {
	_, _, ts := setupTest(t)

	resp, body := req(t, "GET", ts.URL+"/files", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST empty: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	var result listResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("LIST empty: decode: %v", err)
	}
	if len(result.Objects) != 0 {
		t.Errorf("LIST empty: count=%d want 0", len(result.Objects))
	}
	if result.HasMore {
		t.Error("LIST empty: HasMore should be false")
	}
}

// --- Bucket policy enforcement tests -----------------------------------------

// TestBucketPolicyDenyPut verifies that a bucket policy denying s3:PutObject
// blocks PUT requests and returns 403 Forbidden.
func TestBucketPolicyDenyPut(t *testing.T) {
	_, _, ts := setupTest(t)

	// First PUT an object so we can verify read still works.
	base := ts.URL + "/files/policy-test.txt"
	resp, body := req(t, "PUT", base, []byte("hello"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup PUT: status=%d want 201, body=%s", resp.StatusCode, body)
	}

	// Set a policy that Denies s3:PutObject but Allows s3:GetObject.
	policyURL := ts.URL + "/buckets/default/policy"
	denyPutPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/*"},{"Effect":"Deny","Principal":"*","Action":"s3:PutObject","Resource":"arn:aws:s3:::default/*"}]}`
	resp, body = req(t, "PUT", policyURL, bodyPolicy(denyPutPolicy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// PUT should now be denied.
	resp, body = req(t, "PUT", base, []byte("world"), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT after deny policy: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// GET should still work (explicit Allow for s3:GetObject).
	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after deny policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// HEAD should still work.
	resp, body = req(t, "HEAD", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD after deny policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// Clear policy.
	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// TestBucketPolicyDenyGet verifies that a bucket policy denying s3:GetObject
// blocks GET and HEAD requests and returns 403 Forbidden.
func TestBucketPolicyDenyGet(t *testing.T) {
	_, _, ts := setupTest(t)

	// First PUT an object.
	base := ts.URL + "/files/policy-get-test.txt"
	resp, body := req(t, "PUT", base, []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup PUT: status=%d want 201, body=%s", resp.StatusCode, body)
	}

	// Set a policy that explicitly Allows s3:PutObject but Denies s3:GetObject.
	policyURL := ts.URL + "/buckets/default/policy"
	denyGetPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:PutObject","Resource":"arn:aws:s3:::default/*"},{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/*"}]}`
	resp, body = req(t, "PUT", policyURL, bodyPolicy(denyGetPolicy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// GET should be denied.
	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET after deny policy: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// HEAD should also be denied.
	resp, body = req(t, "HEAD", base, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("HEAD after deny policy: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// Clear policy.
	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// TestBucketPolicyDenyDelete verifies that a bucket policy denying
// s3:DeleteObject blocks DELETE requests and returns 403 Forbidden.
func TestBucketPolicyDenyDelete(t *testing.T) {
	_, _, ts := setupTest(t)

	// First PUT an object.
	base := ts.URL + "/files/policy-del-test.txt"
	resp, body := req(t, "PUT", base, []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup PUT: status=%d want 201, body=%s", resp.StatusCode, body)
	}

	// Set a policy that Denies s3:DeleteObject but Allows s3:GetObject.
	policyURL := ts.URL + "/buckets/default/policy"
	denyDelPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::default/*"},{"Effect":"Deny","Principal":"*","Action":"s3:DeleteObject","Resource":"arn:aws:s3:::default/*"}]}`
	resp, body = req(t, "PUT", policyURL, bodyPolicy(denyDelPolicy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// DELETE should be denied.
	resp, body = req(t, "DELETE", base, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("DELETE after deny policy: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// GET should still work.
	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after deny policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// Clear policy.
	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// TestBucketPolicyImplicitDeny verifies that without an explicit Allow
// statement, all actions are implicitly denied.
func TestBucketPolicyImplicitDeny(t *testing.T) {
	_, _, ts := setupTest(t)

	// PUT an object first.
	base := ts.URL + "/files/policy-implicit.txt"
	resp, body := req(t, "PUT", base, []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup PUT: status=%d want 201, body=%s", resp.StatusCode, body)
	}

	// Set a policy with ONLY Allow statements for unrelated actions —
	// s3:PutObject is not mentioned anywhere, so it's implicitly denied.
	policyURL := ts.URL + "/buckets/default/policy"
	onlyGetPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/*"}]}`
	resp, body = req(t, "PUT", policyURL, bodyPolicy(onlyGetPolicy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// PUT should be implicitly denied (no Allow for s3:PutObject).
	resp, body = req(t, "PUT", base, []byte("new data"), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT implicit deny: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// GET should still be explicitly allowed.
	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after explicit allow: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// Clear policy.
	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// TestBucketPolicyList verifies that s3:ListBucket enforcement works for
// the LIST /files endpoint.
func TestBucketPolicyList(t *testing.T) {
	_, _, ts := setupTest(t)

	// PUT an object so there's something to list.
	resp, body := req(t, "PUT", ts.URL+"/files/list-policy-test.txt", []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup PUT: status=%d want 201, body=%s", resp.StatusCode, body)
	}

	// Set a policy that only Allows s3:GetObject — ListBucket is not allowed.
	policyURL := ts.URL + "/buckets/default/policy"
	onlyGetPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/*"}]}`
	resp, body = req(t, "PUT", policyURL, bodyPolicy(onlyGetPolicy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// LIST should be implicitly denied.
	resp, body = req(t, "GET", ts.URL+"/files", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("LIST implicit deny: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// Clear policy.
	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// TestBucketPolicyNoPolicyDoesNotBlock verifies that buckets without a policy
// allow all operations (backward compatibility).
func TestBucketPolicyNoPolicyDoesNotBlock(t *testing.T) {
	_, _, ts := setupTest(t)

	// No policy set — all operations should work normally.
	base := ts.URL + "/files/no-policy.txt"

	resp, body := req(t, "PUT", base, []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT no policy: status=%d want 201, body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET no policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "HEAD", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD no policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "GET", ts.URL+"/files", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST no policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "DELETE", base, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE no policy: status=%d want 204, body=%s", resp.StatusCode, body)
	}
}
