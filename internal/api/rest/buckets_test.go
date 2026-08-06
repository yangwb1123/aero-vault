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

	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newBucketTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "b.db"))
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
	svc := service.NewFileService(store, repo, nil)
	h := NewHandler(svc, nil)
	reg, _ := auth.Parse("")
	adm := NewAdminHandler(svc, repo, reg)

	r := chi.NewRouter()
	r.Get("/v1/buckets/{bucket}/config", h.GetBucketConfig)
	r.Put("/v1/buckets/{bucket}/versioning", h.PutBucketVersioning)
	r.Put("/v1/buckets/{bucket}/object-lock", h.PutBucketLock)
	r.Put("/v1/buckets/{bucket}/lifecycle", adm.PutBucketLifecycle)
	r.Get("/v1/admin/audit", adm.ListAudit)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv
}

func newBucketQuotaAuthTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "quota-auth.db"))
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
	reg, err := auth.Parse("writer:default:write,operator:acme:admin")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	adm := NewAdminHandler(service.NewFileService(store, repo, nil), repo, reg)
	r := chi.NewRouter()
	r.Use(mw.Tenant)
	r.Use(reg.Middleware())
	r.Put("/v1/admin/buckets/{bucket}/quota", adm.PutBucketQuota)
	r.Get("/v1/admin/audit", adm.ListAudit)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv
}

func TestBucketConfig(t *testing.T) {
	srv := newBucketTestServer(t)
	base := srv.URL + "/v1/buckets/default"

	// 1. GET config → 200, defaults
	resp, body := req(t, "GET", base+"/config", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET config: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("GET config: parse JSON: %v", err)
	}
	if cfg["name"] != "default" {
		t.Errorf("name: got %v want default", cfg["name"])
	}
	if v, ok := cfg["versioning"].(bool); !ok || v {
		t.Errorf("versioning: got %v want false", cfg["versioning"])
	}
	if v, ok := cfg["object_lock_seconds"].(float64); !ok || v != 0 {
		t.Errorf("object_lock_seconds: got %v want 0", cfg["object_lock_seconds"])
	}

	// 2. PUT versioning enabled → 200, body has versioning:true
	resp, body = req(t, "PUT", base+"/versioning", []byte(`{"enabled":true}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT versioning: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"versioning":true`) {
		t.Errorf("PUT versioning body missing versioning:true, got: %s", body)
	}

	// 3. GET config → versioning is now true
	_, body = req(t, "GET", base+"/config", nil, nil)
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("GET config after versioning: parse JSON: %v", err)
	}
	if v, ok := cfg["versioning"].(bool); !ok || !v {
		t.Errorf("versioning after enable: got %v want true", cfg["versioning"])
	}

	// 4. PUT object-lock → 200, body has object_lock_seconds:3600
	resp, body = req(t, "PUT", base+"/object-lock", []byte(`{"seconds":3600}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT object-lock: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"object_lock_seconds":3600`) {
		t.Errorf("PUT object-lock body missing object_lock_seconds:3600, got: %s", body)
	}

	// 5. GET config → object_lock_seconds is now 3600
	_, body = req(t, "GET", base+"/config", nil, nil)
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("GET config after lock: parse JSON: %v", err)
	}
	if v, ok := cfg["object_lock_seconds"].(float64); !ok || v != 3600 {
		t.Errorf("object_lock_seconds after set: got %v want 3600", cfg["object_lock_seconds"])
	}

	// 6. PUT versioning invalid JSON → 400
	resp, _ = req(t, "PUT", base+"/versioning", []byte(`not json`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT versioning invalid JSON: status=%d want 400", resp.StatusCode)
	}

	// 7. PUT object-lock negative seconds → 400
	resp, _ = req(t, "PUT", base+"/object-lock", []byte(`{"seconds":-1}`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT object-lock negative seconds: status=%d want 400", resp.StatusCode)
	}

	// 8. PUT lifecycle → 200
	resp, body = req(t, "PUT", base+"/lifecycle", []byte(`{"days":30,"action":"soft_delete"}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT lifecycle: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// 9. GET config → expire_after_days is 30
	_, body = req(t, "GET", base+"/config", nil, nil)
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("GET config after lifecycle: parse JSON: %v", err)
	}
	if v, ok := cfg["expire_after_days"].(float64); !ok || v != 30 {
		t.Errorf("expire_after_days after lifecycle: got %v want 30", cfg["expire_after_days"])
	}

	// Lifecycle changes are intentionally outside the audit contract.
	_, body = req(t, "GET", srv.URL+"/v1/admin/audit", nil, nil)
	var audit struct {
		Entries []repository.AuditEntry `json:"audit"`
	}
	if err := json.Unmarshal(body, &audit); err != nil {
		t.Fatalf("GET audit after lifecycle: parse JSON: %v", err)
	}
	if len(audit.Entries) != 0 {
		t.Fatalf("lifecycle unexpectedly created audit entries: %+v", audit.Entries)
	}
}

func TestAdminBucketQuotaRequiresAdmin(t *testing.T) {
	srv := newBucketQuotaAuthTestServer(t)
	url := srv.URL + "/v1/admin/buckets/default/quota"
	body := []byte(`{"max_bytes":1024,"max_objects":10}`)

	writerHeaders := map[string]string{"Authorization": "Bearer writer", mw.TenantHeader: "acme"}
	resp, got := req(t, http.MethodPut, url, body, writerHeaders)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write-only key: status=%d want 403, body=%s", resp.StatusCode, got)
	}

	operatorHeaders := map[string]string{"Authorization": "Bearer operator", mw.TenantHeader: "acme"}
	resp, got = req(t, http.MethodPut, url, body, operatorHeaders)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin key: status=%d want 200, body=%s", resp.StatusCode, got)
	}

	resp, got = req(t, http.MethodGet, srv.URL+"/v1/admin/audit", nil, operatorHeaders)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit list: status=%d body=%s", resp.StatusCode, got)
	}
	var audit struct {
		Entries []repository.AuditEntry `json:"audit"`
	}
	if err := json.Unmarshal(got, &audit); err != nil || len(audit.Entries) != 1 {
		t.Fatalf("audit entries=%+v err=%v", audit.Entries, err)
	}
	if audit.Entries[0].TenantID != "acme" || audit.Entries[0].Target != "default" {
		t.Fatalf("bucket quota audit attribution=%+v", audit.Entries[0])
	}
}
