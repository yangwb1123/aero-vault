package s3compat

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newManagedSSETestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "sse.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{
		Root: filepath.Join(dir, "objects"), SSEKey: "managed-sse-test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRouter(service.NewFileService(store, repo, nil), nil, nil)) // SSE PUT/GET only; delete gate irrelevant
	t.Cleanup(func() {
		srv.Close()
		_ = repo.Close()
	})
	return srv
}

func TestManagedSSEBucketDefaultAndRequestHeaders(t *testing.T) {
	srv := newManagedSSETestServer(t)
	base := srv.URL + "/encrypted"
	do(t, http.MethodPut, base, nil, nil)
	config, _ := xml.Marshal(serverSideEncryptionConfiguration{
		Rules: []serverSideEncryptionRule{{Apply: serverSideEncryptionApply{SSEAlgorithm: "AES256"}}},
	})
	resp, body := do(t, http.MethodPut, base+"?encryption", config, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put encryption status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodPut, base+"/default.txt", []byte("secret"), nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get(managedSSEHeader) != "AES256" {
		t.Fatalf("bucket-default PUT status=%d sse=%q body=%s",
			resp.StatusCode, resp.Header.Get(managedSSEHeader), body)
	}
	resp, body = do(t, http.MethodGet, base+"/default.txt", nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "secret" ||
		resp.Header.Get(managedSSEHeader) != "AES256" {
		t.Fatalf("bucket-default GET status=%d sse=%q body=%q",
			resp.StatusCode, resp.Header.Get(managedSSEHeader), body)
	}

	resp, body = do(t, http.MethodPut, base+"/explicit.txt", []byte("value"), map[string]string{
		managedSSEHeader: "AES256",
	})
	if resp.StatusCode != http.StatusOK || resp.Header.Get(managedSSEHeader) != "AES256" {
		t.Fatalf("explicit SSE PUT status=%d sse=%q body=%s",
			resp.StatusCode, resp.Header.Get(managedSSEHeader), body)
	}

	multipartURL := base + "/multipart.bin"
	resp, body = do(t, http.MethodPost, multipartURL+"?uploads", nil, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get(managedSSEHeader) != "AES256" {
		t.Fatalf("managed SSE multipart init status=%d sse=%q body=%s",
			resp.StatusCode, resp.Header.Get(managedSSEHeader), body)
	}
	var initialized initiateMultipartUploadResult
	if err := xml.Unmarshal(body, &initialized); err != nil {
		t.Fatal(err)
	}
	resp, _ = do(t, http.MethodPut,
		multipartURL+"?partNumber=1&uploadId="+initialized.UploadID, []byte("part"), nil)
	manifest, _ := xml.Marshal(completeMultipartUpload{
		Parts: []completePartItem{{PartNumber: 1, ETag: resp.Header.Get("ETag")}},
	})
	resp, body = do(t, http.MethodPost,
		multipartURL+"?uploadId="+initialized.UploadID, manifest, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get(managedSSEHeader) != "AES256" {
		t.Fatalf("managed SSE multipart complete status=%d sse=%q body=%s",
			resp.StatusCode, resp.Header.Get(managedSSEHeader), body)
	}
}

func TestManagedSSEFailsClosedWhenBackendCannotEncrypt(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/plain"
	do(t, http.MethodPut, base, nil, nil)
	config, _ := xml.Marshal(serverSideEncryptionConfiguration{
		Rules: []serverSideEncryptionRule{{Apply: serverSideEncryptionApply{SSEAlgorithm: "AES256"}}},
	})
	resp, body := do(t, http.MethodPut, base+"?encryption", config, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported bucket SSE status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodPut, base+"/object.txt", []byte("x"), map[string]string{
		managedSSEHeader: "AES256",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported request SSE status=%d body=%s", resp.StatusCode, body)
	}
	resp, _ = do(t, http.MethodHead, base+"/object.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("failed SSE write left an object: status=%d", resp.StatusCode)
	}
}

func TestManagedSSERejectsConflictingSSEC(t *testing.T) {
	srv := newManagedSSETestServer(t)
	headers := ssecTestHeaders([]byte("0123456789abcdef0123456789abcdef"))
	headers[managedSSEHeader] = "AES256"
	resp, body := do(t, http.MethodPut, srv.URL+"/encrypted/conflict.txt", []byte("x"), headers)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("conflicting SSE status=%d body=%s", resp.StatusCode, body)
	}
}
