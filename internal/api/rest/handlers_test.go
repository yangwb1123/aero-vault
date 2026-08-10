package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"log/slog"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
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
	return setupTestWith(t)
}

// setupTestWith is setupTest plus Handler options (e.g. presign capability).
func setupTestWith(t *testing.T, opts ...func(*Handler)) (*service.FileService, repository.Repository, *httptest.Server) {
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
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	router := NewRouter(svc, repo, nil, nil, nil, nil, nil, slog.Default(), false, nil, nil, 0, false, opts...)
	ts := httptest.NewServer(router)
	t.Cleanup(func() { ts.Close(); _ = repo.Close() })
	return svc, repo, ts
}

// allowAllProvider is the CI-baseline test double injected into helpers that
// construct a bare FileService: it preserves the pre-fail-closed baseline
// (all actions allowed) for tests exercising non-authz behavior. The
// fail-closed delete gate itself is covered by dedicated tests
// (authz_delete_denied_test.go and the T-* gate tests).

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

func TestContentMD5MismatchReturnsBadDigest(t *testing.T) {
	_, _, ts := setupTest(t)
	resp, body := req(
		t, http.MethodPut, ts.URL+"/files/bad-md5.txt", []byte("content"),
		map[string]string{"Content-MD5": "AAAAAAAAAAAAAAAAAAAAAA=="},
	)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT status=%d, want 400; body=%s", resp.StatusCode, body)
	}
	var got errorBody
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != "BadDigest" {
		t.Fatalf("error code=%q, want BadDigest", got.Error.Code)
	}
}

func TestMetadataLimitErrorsAreBadRequests(t *testing.T) {
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
	resp, body = req(t, "POST", base+"/presign?op=put", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("presign PUT after deny policy: status=%d want 403, body=%s", resp.StatusCode, body)
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
	resp, body = req(t, "POST", base+"/presign?op=get", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("presign GET after deny policy: status=%d want 403, body=%s", resp.StatusCode, body)
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

func TestBucketPolicyRejectsInvalidDocument(t *testing.T) {
	_, repo, ts := setupTest(t)
	policyURL := ts.URL + "/buckets/default/policy"

	resp, body := req(t, "PUT", policyURL, bodyPolicy(`{"Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Condition":{"Bool":{"aws:SecureTransport":["true"]}}}]}`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy status=%d want 400, body=%s", resp.StatusCode, body)
	}
	cfg, err := repo.GetBucketConfig(context.Background(), "default", "default")
	if err != nil {
		t.Fatalf("get bucket config: %v", err)
	}
	if cfg.Policy != "" {
		t.Fatalf("invalid policy was persisted: %s", cfg.Policy)
	}
}

type allowAllProvider struct{}

func (allowAllProvider) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: true, Reason: "test_allow_all"}, nil
}

// ── Bucket-policy Resource constraints (bucket-policy-rest-resource-v1) ─────

// AC-2: REST must enforce key-scoped Allow policies. Out-of-scope keys are
// 403 (pre-fix they were 200 — fail-open bypass); in-scope keys stay 200.
func TestBucketPolicyResourceScopedAllow(t *testing.T) {
	_, _, ts := setupTest(t)

	// 1) Create objects first: after the policy is installed PUT of an
	// out-of-scope key is itself rejected, so it can't be used for setup.
	for _, k := range []string{"secret/key1", "other"} {
		resp, body := req(t, "PUT", ts.URL+"/files/"+k, []byte("data"), nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("setup PUT %s: status=%d want 201, body=%s", k, resp.StatusCode, body)
		}
	}

	// 2) Install key-scoped Allow policy: only secret/* readable.
	policyURL := ts.URL + "/buckets/default/policy"
	scopedPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*",
		"Action":"s3:GetObject","Resource":["arn:aws:s3:::default/secret/*"]}]}`
	resp, body := req(t, "PUT", policyURL, bodyPolicy(scopedPolicy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// 3) Out-of-scope key: 403 (pre-fix fail-open returned 200).
	resp, body = req(t, "GET", ts.URL+"/files/other", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET other after scoped Allow: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// 4) In-scope key: 200.
	resp, body = req(t, "GET", ts.URL+"/files/secret/key1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET secret/key1 after scoped Allow: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// 5) Clear policy.
	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// AC-2b: key-scoped Deny must only reject matching keys — the divergence leg
// of the pre-fix empty-resource eval (which denied everything).
func TestBucketPolicyResourceScopedDeny(t *testing.T) {
	_, _, ts := setupTest(t)

	for _, k := range []string{"secret/key1", "other"} {
		resp, body := req(t, "PUT", ts.URL+"/files/"+k, []byte("data"), nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("setup PUT %s: status=%d want 201, body=%s", k, resp.StatusCode, body)
		}
	}

	policyURL := ts.URL + "/buckets/default/policy"
	policy := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/*"},
		{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":["arn:aws:s3:::default/secret/*"]}]}`
	resp, body := req(t, "PUT", policyURL, bodyPolicy(policy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// Out-of-scope key: allowed (pre-fix empty-resource eval denied everything).
	resp, body = req(t, "GET", ts.URL+"/files/other", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET other under scoped Deny: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// In-scope key: denied.
	resp, body = req(t, "GET", ts.URL+"/files/secret/key1", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET secret/key1 under scoped Deny: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// QA P0: ListBucket is a bucket-level action evaluated against the bucket ARN.
// `Allow ListBucket Resource default/*` flips 200→403 (converging with /s3 and
// AWS — the one documented compat break), while bucket-ARN and "*" still
// allow. Also locks the empty-key route (GET /files/ → key "" → bucket ARN).
func TestBucketPolicyListBucketARN(t *testing.T) {
	_, _, ts := setupTest(t)
	policyURL := ts.URL + "/buckets/default/policy"

	putPolicy := func(p string) {
		t.Helper()
		resp, body := req(t, "PUT", policyURL, bodyPolicy(p), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
		}
	}

	// (a) Object-scoped pattern does not cover the bucket: 403 (flip detector —
	// pre-fix empty-resource eval returned 200).
	putPolicy(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:ListBucket","Resource":"arn:aws:s3:::default/*"}]}`)
	resp, body := req(t, "GET", ts.URL+"/files", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ListBucket under default/* Allow: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// (b) Bucket ARN allows listing.
	putPolicy(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:ListBucket","Resource":"arn:aws:s3:::default"}]}`)
	resp, body = req(t, "GET", ts.URL+"/files", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ListBucket under bucket-ARN Allow: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// (c) "*" allows listing.
	putPolicy(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:ListBucket","Resource":"*"}]}`)
	resp, body = req(t, "GET", ts.URL+"/files", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ListBucket under * Allow: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// (d) Empty-key object route (GET /files/) evaluates the bucket ARN:
	// object-scoped GetObject Allow no longer matches → 403 at the policy gate.
	putPolicy(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/*"}]}`)
	resp, body = req(t, "GET", ts.URL+"/files/", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /files/ under default/* GetObject Allow: status=%d want 403, body=%s", resp.StatusCode, body)
	}
	// Bucket-ARN Allow passes the policy gate; the service then rejects the
	// empty key itself (400), which is fine — the gate must not be the blocker.
	putPolicy(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default"}]}`)
	resp, body = req(t, "GET", ts.URL+"/files/", nil, nil)
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("GET /files/ under bucket-ARN Allow must pass the policy gate, got 403, body=%s", body)
	}

	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// QA P0: a stored-but-unparseable policy must fail closed (403) on every REST
// object operation — mirrors s3compat's stored-invalid fail-closed test.
func TestBucketPolicyStoredInvalidFailsClosed(t *testing.T) {
	_, repo, ts := setupTest(t)

	resp, body := req(t, "PUT", ts.URL+"/files/init.txt", []byte("init"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup PUT: status=%d want 201, body=%s", resp.StatusCode, body)
	}

	// Rejected write must not persist.
	resp, body = req(t, "PUT", ts.URL+"/buckets/default/policy", bodyPolicy(`{"Statement":`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy write: status=%d want 400, body=%s", resp.StatusCode, body)
	}
	resp, body = req(t, "GET", ts.URL+"/files/init.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rejected policy must not persist, GET: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// Seed a malformed legacy row directly, as if written before validation.
	if err := repo.SetBucketPolicy(context.Background(), "default", "default", `{"Statement":`); err != nil {
		t.Fatalf("seed invalid policy: %v", err)
	}
	for _, tt := range []struct {
		method, path string
	}{
		{"GET", "/files/init.txt"},
		{"HEAD", "/files/init.txt"},
		{"PUT", "/files/init.txt"},
		{"DELETE", "/files/init.txt"},
	} {
		resp, body = req(t, tt.method, ts.URL+tt.path, []byte("data"), nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s under stored invalid policy: status=%d want 403, body=%s", tt.method, tt.path, resp.StatusCode, body)
		}
	}
}

// AC-4: the policy write path rejects invalid Effect/Principal with 400, the
// body carries the offending statement index, and nothing is persisted.
func TestBucketPolicyRejectsInvalidEffectAndPrincipal(t *testing.T) {
	_, repo, ts := setupTest(t)
	policyURL := ts.URL + "/buckets/default/policy"

	// Bad Effect in the second statement (index 1) — body must name it.
	badEffect := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"},
		{"Effect":"deny","Principal":"*","Action":"s3:PutObject"}]}`
	resp, body := req(t, "PUT", policyURL, bodyPolicy(badEffect), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad Effect: status=%d want 400, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "statement 1") {
		t.Fatalf("bad Effect: body=%s want it to name statement 1", body)
	}

	// Invalid (non-wildcard) Principal.
	badPrincipal := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123:root"},"Action":"s3:*"}]}`
	resp, body = req(t, "PUT", policyURL, bodyPolicy(badPrincipal), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad Principal: status=%d want 400, body=%s", resp.StatusCode, body)
	}

	// Neither may be persisted.
	cfg, err := repo.GetBucketConfig(context.Background(), "default", "default")
	if err != nil {
		t.Fatalf("get bucket config: %v", err)
	}
	if cfg.Policy != "" {
		t.Fatalf("invalid policy was persisted: %s", cfg.Policy)
	}
}

// AC-5: the REST ARN builder must mirror s3compat s3ResourceARN byte-for-byte.
func TestBucketPolicyResourceARNFormat(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"", "arn:aws:s3:::default"},
		{"secret/key1", "arn:aws:s3:::default/secret/key1"},
		{"a", "arn:aws:s3:::default/a"},
		{"dir/with space", "arn:aws:s3:::default/dir/with space"},
	}
	for _, tt := range tests {
		if got := bucketPolicyResourceARN(tt.key); got != tt.want {
			t.Errorf("bucketPolicyResourceARN(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

// QA P1: every policy call site (Put/Get/Head/Delete/PostForm/presign) must
// evaluate the ARN of the key it actually operates on. The PostForm key comes
// from the form (with filename fallback) and the presign key strips the
// /presign suffix — wiring bugs here would let a scoped Deny be bypassed via
// an attacker-controlled filename.
func TestBucketPolicyScopedAllCallSites(t *testing.T) {
	// Presign signer so in-scope presign requests complete past the policy gate.
	_, _, ts := setupTestWith(t, func(h *Handler) {
		h.WithPutPresigner(auth.NewPutPresigner("0123456789abcdef0123456789abcdef"))
	})

	// Create in-scope objects before installing the policy (out-of-scope PUTs
	// will be rejected afterwards).
	for _, k := range []string{"secret/a", "secret/b", "secret/c.txt", "secret/d.txt"} {
		resp, body := req(t, "PUT", ts.URL+"/files/"+k, []byte("data"), nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("setup PUT %s: status=%d want 201, body=%s", k, resp.StatusCode, body)
		}
	}

	policyURL := ts.URL + "/buckets/default/policy"
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*",
		"Action":["s3:PutObject","s3:GetObject","s3:DeleteObject"],
		"Resource":"arn:aws:s3:::default/secret/*"}]}`
	resp, body := req(t, "PUT", policyURL, bodyPolicy(policy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	scope := func(method, path string, want int) {
		t.Helper()
		resp, body := req(t, method, ts.URL+path, []byte("data"), nil)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d want %d, body=%s", method, path, resp.StatusCode, want, body)
		}
	}

	// Path CRUD matrix: in-scope allowed, out-of-scope 403.
	scope("PUT", "/files/secret/new.txt", http.StatusCreated)
	scope("PUT", "/files/other/new.txt", http.StatusForbidden)
	scope("GET", "/files/secret/a", http.StatusOK)
	scope("GET", "/files/other/a", http.StatusForbidden)
	scope("HEAD", "/files/secret/a", http.StatusOK)
	scope("HEAD", "/files/other/a", http.StatusForbidden)
	scope("DELETE", "/files/secret/b", http.StatusNoContent)
	scope("DELETE", "/files/other/b", http.StatusForbidden)

	// PostForm with explicit key field.
	form := func(key, filename string) []byte {
		b := "--tb\r\n" +
			`Content-Disposition: form-data; name="key"` + "\r\n\r\n" + key + "\r\n" +
			"--tb\r\n" +
			`Content-Disposition: form-data; name="file"; filename="` + filename + `"` + "\r\n" +
			"Content-Type: text/plain\r\n\r\n" +
			"form data\r\n" +
			"--tb--\r\n"
		return []byte(b)
	}
	postForm := func(key, filename string, want int) {
		t.Helper()
		resp, body := req(t, "POST", ts.URL+"/files", form(key, filename), map[string]string{
			"Content-Type": "multipart/form-data; boundary=tb",
		})
		if resp.StatusCode != want {
			t.Fatalf("POST form key=%q filename=%q: status=%d want %d, body=%s", key, filename, resp.StatusCode, want, body)
		}
	}
	postForm("secret/e", "ignored.txt", http.StatusCreated)  // form key wins
	postForm("other/e", "ignored.txt", http.StatusForbidden) // scoped Deny via form key

	// Filename fallback: no key field — the stored key is filepath.Base(filename)
	// (Go multipart strips directory info), and the policy must be evaluated
	// against that SAME stored key. filename="secret/f.txt" lands at "f.txt":
	// if the gate evaluated the raw (in-scope) filename instead, this would
	// 201 and leave an unchecked object at f.txt — the bypass this locks.
	formNoKey := func(filename string) []byte {
		b := "--tb\r\n" +
			`Content-Disposition: form-data; name="file"; filename="` + filename + `"` + "\r\n" +
			"Content-Type: text/plain\r\n\r\n" +
			"form data\r\n" +
			"--tb--\r\n"
		return []byte(b)
	}
	postFormNoKey := func(filename string, want int) {
		t.Helper()
		resp, body := req(t, "POST", ts.URL+"/files", formNoKey(filename), map[string]string{
			"Content-Type": "multipart/form-data; boundary=tb",
		})
		if resp.StatusCode != want {
			t.Fatalf("POST form filename=%q: status=%d want %d, body=%s", filename, resp.StatusCode, want, body)
		}
	}
	postFormNoKey("secret/f.txt", http.StatusForbidden) // stored key f.txt is out of scope
	resp, body = req(t, "GET", ts.URL+"/files/f.txt", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET f.txt (basename of fallback filename): status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// Presign: the key is the URL path minus the /presign suffix.
	presign := func(path, op string, want int) {
		t.Helper()
		resp, body := req(t, "POST", ts.URL+path+"?op="+op, nil, nil)
		if resp.StatusCode != want {
			t.Fatalf("presign %s op=%s: status=%d want %d, body=%s", path, op, resp.StatusCode, want, body)
		}
	}
	presign("/files/secret/c.txt/presign", "get", http.StatusOK)
	presign("/files/other/c.txt/presign", "get", http.StatusForbidden)
	presign("/files/secret/d.txt/presign", "put", http.StatusOK)
	presign("/files/other/d.txt/presign", "put", http.StatusForbidden)

	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}

// QA P1: the policy gate runs before the anonymous/public-share allowance, so
// a scoped Allow cannot be bypassed through a public ACL on an out-of-scope
// object. Pre-fix this returned 200 (policy passed via empty resource, then
// allowAnonymous granted access); post-fix it is 403.
func TestBucketPolicyGatesBeforePublicShare(t *testing.T) {
	_, svc, ts := setupTest(t)

	for _, k := range []string{"secret/pub.txt", "other/pub.txt"} {
		resp, body := req(t, "PUT", ts.URL+"/files/"+k, []byte("data"), nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("setup PUT %s: status=%d want 201, body=%s", k, resp.StatusCode, body)
		}
		if err := svc.SetObjectACL(context.Background(), "default", "default", k, "public-read"); err != nil {
			t.Fatalf("set public ACL on %s: %v", k, err)
		}
	}

	policyURL := ts.URL + "/buckets/default/policy"
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*",
		"Action":"s3:GetObject","Resource":"arn:aws:s3:::default/secret/*"}]}`
	resp, body := req(t, "PUT", policyURL, bodyPolicy(policy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// Anonymous (no auth context in these requests) in-scope public object: 200.
	resp, body = req(t, "GET", ts.URL+"/files/secret/pub.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous GET in-scope public: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	// Anonymous out-of-scope public object: 403 at the policy gate (pre-fix 200).
	resp, body = req(t, "GET", ts.URL+"/files/other/pub.txt", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous GET out-of-scope public: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}
