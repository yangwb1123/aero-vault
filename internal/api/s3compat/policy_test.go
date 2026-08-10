package s3compat

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestBucketPolicyProtectsBucketSubresources(t *testing.T) {
	s := newTestServer(t)
	base := s.URL + "/bucket"
	resp, _ := do(t, http.MethodPut, base+"/init.txt", []byte("init"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init status = %d", resp.StatusCode)
	}

	policy := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Principal":"*","Action":"s3:*","Resource":"*"},` +
		`{"Effect":"Deny","Principal":"*","Action":"s3:GetBucketVersioning",` +
		`"Resource":"arn:aws:s3:::bucket"}]}`
	resp, body := do(t, http.MethodPut, base+"/?policy", []byte(policy), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put policy status = %d, body = %s", resp.StatusCode, body)
	}

	resp, _ = do(t, http.MethodGet, base+"?versioning", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("versioning status = %d, want 403", resp.StatusCode)
	}
}

func TestBucketPolicyResourceMismatchIsDenied(t *testing.T) {
	s := newTestServer(t)
	base := s.URL + "/bucket"
	for key, content := range map[string]string{
		"allowed/nested.txt": "allowed",
		"blocked.txt":        "blocked",
	} {
		resp, _ := do(t, http.MethodPut, base+"/"+key, []byte(content), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("put %s status = %d", key, resp.StatusCode)
		}
	}

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
		`"Principal":"*","Action":"s3:GetObject",` +
		`"Resource":"arn:aws:s3:::bucket/allowed/*"}]}`
	resp, body := do(t, http.MethodPut, base+"/?policy", []byte(policy), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put policy status = %d, body = %s", resp.StatusCode, body)
	}

	resp, body = do(t, http.MethodGet, base+"/allowed/nested.txt", nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "allowed" {
		t.Fatalf("allowed resource status = %d, body = %q", resp.StatusCode, body)
	}
	resp, _ = do(t, http.MethodGet, base+"/blocked.txt", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mismatched resource status = %d, want 403", resp.StatusCode)
	}
}

func TestInvalidBucketPolicyRejectedAndStoredInvalidPolicyFailsClosed(t *testing.T) {
	s, svc := newPolicyTestServer(t)
	base := s.URL + "/bucket"
	resp, _ := do(t, http.MethodPut, base+"/init.txt", []byte("init"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init status = %d", resp.StatusCode)
	}

	resp, _ = do(t, http.MethodPut, base+"/?policy", []byte(`{"Statement":`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy write status = %d, want 400", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodGet, base+"/init.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rejected policy must not persist, GET status = %d", resp.StatusCode)
	}

	// Simulate a malformed legacy row written before policy validation existed.
	if err := svc.Repo().SetBucketPolicy(context.Background(), "default", "bucket", `{"Statement":`); err != nil {
		t.Fatalf("seed invalid policy: %v", err)
	}
	resp, _ = do(t, http.MethodGet, base+"/init.txt", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stored invalid policy status = %d, want 403", resp.StatusCode)
	}
}

func TestPutBucketPolicyRejectsInvalidEffect(t *testing.T) {
	s := newTestServer(t)
	base := s.URL + "/bucket"
	resp, _ := do(t, http.MethodPut, base+"/init.txt", []byte("init"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init status = %d", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodPut, base+"/?policy",
		[]byte(`{"Statement":[{"Effect":"deny","Action":"s3:*"}]}`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid Effect write status = %d, want 400", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodGet, base+"/init.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rejected policy must not persist, GET status = %d", resp.StatusCode)
	}
}

func TestSSECPutGetHeadAndRange(t *testing.T) {
	s := newTestServer(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	headers := ssecTestHeaders(key)
	url := s.URL + "/bucket/ssec.txt"

	resp, body := do(t, http.MethodPut, url, []byte("secret"), headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE-C PUT status = %d; body = %s", resp.StatusCode, body)
	}
	assertSSECResponseHeaders(t, resp, headers)
	if resp.Header.Get(ssecHeaderPrefix+"-key") != "" {
		t.Fatal("SSE-C response must never expose the customer key")
	}

	resp, _ = do(t, http.MethodGet, url, nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("SSE-C GET without key status = %d, want 400", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodGet, url, nil, ssecTestHeaders([]byte("abcdef0123456789abcdef0123456789")))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("SSE-C GET with wrong key status = %d, want 400", resp.StatusCode)
	}

	resp, body = do(t, http.MethodGet, url, nil, headers)
	if resp.StatusCode != http.StatusOK || string(body) != "secret" {
		t.Fatalf("SSE-C GET status = %d, body = %q", resp.StatusCode, body)
	}
	assertSSECResponseHeaders(t, resp, headers)

	resp, _ = do(t, http.MethodHead, url, nil, headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE-C HEAD status = %d", resp.StatusCode)
	}
	assertSSECResponseHeaders(t, resp, headers)

	rangeHeaders := ssecTestHeaders(key)
	rangeHeaders["Range"] = "bytes=1-3"
	resp, body = do(t, http.MethodGet, url, nil, rangeHeaders)
	if resp.StatusCode != http.StatusPartialContent || string(body) != "ecr" {
		t.Fatalf("SSE-C range status = %d, body = %q", resp.StatusCode, body)
	}
}

func TestSSECRejectsInvalidHeadersBeforeWriting(t *testing.T) {
	s := newTestServer(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	headers := ssecTestHeaders(key)
	headers[ssecHeaderPrefix+"-key-MD5"] = base64.StdEncoding.EncodeToString(make([]byte, md5.Size))
	url := s.URL + "/bucket/rejected.txt"

	resp, _ := do(t, http.MethodPut, url, []byte("secret"), headers)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid SSE-C MD5 status = %d, want 400", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodGet, url, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("invalid SSE-C request created object, GET status = %d", resp.StatusCode)
	}
}

func TestSSECHeadersRejectedForPlaintextObject(t *testing.T) {
	s := newTestServer(t)
	url := s.URL + "/bucket/plain.txt"
	resp, _ := do(t, http.MethodPut, url, []byte("plain"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plain PUT status = %d", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodGet, url, nil, ssecTestHeaders([]byte("0123456789abcdef0123456789abcdef")))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plaintext GET with SSE-C status = %d, want 400", resp.StatusCode)
	}
}

func ssecTestHeaders(key []byte) map[string]string {
	sum := md5.Sum(key)
	return map[string]string{
		ssecHeaderPrefix + "-algorithm": "AES256",
		ssecHeaderPrefix + "-key":       base64.StdEncoding.EncodeToString(key),
		ssecHeaderPrefix + "-key-MD5":   base64.StdEncoding.EncodeToString(sum[:]),
	}
}

func assertSSECResponseHeaders(t *testing.T, response *http.Response, request map[string]string) {
	t.Helper()
	if got := response.Header.Get(ssecHeaderPrefix + "-algorithm"); got != "AES256" {
		t.Fatalf("SSE-C algorithm response = %q", got)
	}
	if got := response.Header.Get(ssecHeaderPrefix + "-key-MD5"); got != request[ssecHeaderPrefix+"-key-MD5"] {
		t.Fatalf("SSE-C MD5 response = %q", got)
	}
}

func newPolicyTestServer(t *testing.T) (*httptest.Server, *service.FileService) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "policy.db"))
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
	srv := httptest.NewServer(NewRouter(svc, nil, nil)) // nil provider: delete gate defaults to deny; policy tests don't delete
	t.Cleanup(func() {
		srv.Close()
		_ = repo.Close()
	})
	return srv, svc
}
