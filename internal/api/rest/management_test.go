package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newManagementRESTTest(t *testing.T) *httptest.Server {
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
		t.Fatalf("storage: %v", err)
	}
	h := NewHandler(service.NewFileService(store, repo, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Post("/v1/files/*", h.postKey)
	r.Delete("/v1/files/*", h.deleteKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv
}

func TestListVersions(t *testing.T) {
	srv := newManagementRESTTest(t)

	// PUT creates the object (one version).
	putURL := srv.URL + "/v1/files/doc.txt"
	resp, _ := req(t, "PUT", putURL, []byte("hello versions"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d want 201", resp.StatusCode)
	}

	// GET versions returns 200 with one entry.
	resp, body := req(t, "GET", putURL+"/versions", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET versions: status=%d want 200 body=%s", resp.StatusCode, body)
	}

	var result struct {
		Versions []json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal versions response: %v — body=%s", err, body)
	}
	if len(result.Versions) != 1 {
		t.Fatalf("versions count=%d want 1", len(result.Versions))
	}

	// Each version must have version_id, size, etag.
	var v struct {
		VersionID string `json:"version_id"`
		Size      int64  `json:"size"`
		ETag      string `json:"etag"`
	}
	if err := json.Unmarshal(result.Versions[0], &v); err != nil {
		t.Fatalf("unmarshal version entry: %v", err)
	}
	if v.VersionID == "" {
		t.Error("version_id is empty")
	}
	if v.Size == 0 {
		t.Error("size is 0")
	}
	if v.ETag == "" {
		t.Error("etag is empty")
	}

	// Non-existent key → 200 with empty versions list (no rows, no error).
	resp, body = req(t, "GET", srv.URL+"/v1/files/nonexistent.txt/versions", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("versions of missing key: status=%d want 200 body=%s", resp.StatusCode, body)
	}
	var emptyResult struct {
		Versions []json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(body, &emptyResult); err != nil {
		t.Fatalf("unmarshal empty versions: %v — body=%s", err, body)
	}
	if len(emptyResult.Versions) != 0 {
		t.Fatalf("nonexistent key versions count=%d want 0", len(emptyResult.Versions))
	}
}

func TestLockObject(t *testing.T) {
	srv := newManagementRESTTest(t)

	// PUT the object to lock.
	putURL := srv.URL + "/v1/files/locked.txt"
	resp, _ := req(t, "PUT", putURL, []byte("locked content"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d want 201", resp.StatusCode)
	}

	// Lock it for 1 hour.
	resp, body := req(t, "POST", putURL+"/lock", []byte(`{"seconds":3600}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lock: status=%d want 200 body=%s", resp.StatusCode, body)
	}
	var lockResp struct {
		LockedUntil string `json:"locked_until"`
	}
	if err := json.Unmarshal(body, &lockResp); err != nil {
		t.Fatalf("unmarshal lock response: %v — body=%s", err, body)
	}
	if lockResp.LockedUntil == "" {
		t.Error("locked_until is empty")
	}

	// Hard-DELETE a locked object → 409 (soft delete ignores lock; use ?hard=1).
	resp, _ = req(t, "DELETE", putURL+"?hard=1", nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("hard delete locked: status=%d want 409", resp.StatusCode)
	}

	// seconds=0 → 400.
	resp, _ = req(t, "POST", putURL+"/lock", []byte(`{"seconds":0}`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("lock seconds=0: status=%d want 400", resp.StatusCode)
	}

	// Non-existent key → 404.
	resp, _ = req(t, "POST", srv.URL+"/v1/files/nonexistent.txt/lock", []byte(`{"seconds":60}`), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("lock missing key: status=%d want 404", resp.StatusCode)
	}
}

func TestPresignInvalidOp(t *testing.T) {
	srv := newManagementRESTTest(t)

	// PUT the object first so the key exists.
	putURL := srv.URL + "/v1/files/x.txt"
	resp, _ := req(t, "PUT", putURL, []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d want 201", resp.StatusCode)
	}

	// POST presign with an unsupported op → 400.
	resp, body := req(t, "POST", putURL+"/presign?op=bogus", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("presign bogus op: status=%d want 400 body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "op must be") {
		t.Fatalf("presign bogus op body missing error detail: %s", body)
	}
}
