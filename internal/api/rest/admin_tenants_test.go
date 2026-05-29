package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// newAdminTenantTest mounts the tenant admin routes with a disabled (no-auth)
// registry, so requireAdmin passes through and we exercise the handlers
// directly over HTTP.
func newAdminTenantTest(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "tenants.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg, err := auth.Parse("") // disabled → caller is implicitly admin
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	adm := NewAdminHandler(nil, repo, reg)

	r := chi.NewRouter()
	r.Post("/v1/admin/tenants", adm.CreateTenant)
	r.Get("/v1/admin/tenants", adm.ListTenants)
	r.Delete("/v1/admin/tenants/{tenant}", adm.DeleteTenant)
	r.Put("/v1/admin/tenants/{tenant}/status", adm.SetTenantStatus)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv
}

func TestAdminTenantCRUD(t *testing.T) {
	s := newAdminTenantTest(t)
	base := s.URL + "/v1/admin/tenants"

	// Create.
	resp, body := req(t, "POST", base, []byte(`{"tenant_id":"acme","display_name":"Acme Corp"}`), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", resp.StatusCode, body)
	}
	var created map[string]any
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create decode: %v", err)
	}
	if created["tenant_id"] != "acme" || created["display_name"] != "Acme Corp" || created["status"] != "active" {
		t.Fatalf("create payload: %v", created)
	}

	// Empty tenant_id → 400.
	if resp, _ := req(t, "POST", base, []byte(`{"display_name":"x"}`), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create empty id: status=%d want 400", resp.StatusCode)
	}

	// List contains it.
	resp, body = req(t, "GET", base, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status=%d", resp.StatusCode)
	}
	var listed struct {
		Tenants []map[string]any `json:"tenants"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(listed.Tenants) != 1 || listed.Tenants[0]["tenant_id"] != "acme" {
		t.Fatalf("list payload: %v", listed.Tenants)
	}

	// Set status disabled.
	resp, body = req(t, "PUT", base+"/acme/status", []byte(`{"status":"disabled"}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set status: status=%d body=%s", resp.StatusCode, body)
	}
	var statusRec map[string]any
	if err := json.Unmarshal(body, &statusRec); err != nil {
		t.Fatalf("status decode: %v", err)
	}
	if statusRec["status"] != "disabled" || statusRec["display_name"] != "Acme Corp" {
		t.Fatalf("status payload (display_name should be preserved): %v", statusRec)
	}

	// Invalid status → 400.
	if resp, _ := req(t, "PUT", base+"/acme/status", []byte(`{"status":"bogus"}`), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid status: status=%d want 400", resp.StatusCode)
	}

	// Status on unknown tenant → 404.
	if resp, _ := req(t, "PUT", base+"/ghost/status", []byte(`{"status":"active"}`), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status unknown: status=%d want 404", resp.StatusCode)
	}

	// Delete → 204.
	if resp, _ := req(t, "DELETE", base+"/acme", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status=%d want 204", resp.StatusCode)
	}

	// Delete again → 404.
	if resp, _ := req(t, "DELETE", base+"/acme", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again: status=%d want 404", resp.StatusCode)
	}
}
