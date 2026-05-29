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

// newAdminAuditTest mounts the tenant-create and audit routes with a disabled
// (no-auth) registry, so requireAdmin passes through and the handlers run
// directly over HTTP.
func newAdminAuditTest(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "audit.db"))
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
	r.Get("/v1/admin/audit", adm.ListAudit)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv
}

// TestAdminAuditTrail drives a tenant-create mutation, then lists the audit log
// and asserts the corresponding entry appears.
func TestAdminAuditTrail(t *testing.T) {
	s := newAdminAuditTest(t)

	resp, body := req(t, "POST", s.URL+"/v1/admin/tenants", []byte(`{"tenant_id":"acme","display_name":"Acme Corp"}`), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "GET", s.URL+"/v1/admin/audit", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit: status=%d body=%s", resp.StatusCode, body)
	}
	var out struct {
		Audit []repository.AuditEntry `json:"audit"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("audit decode: %v", err)
	}

	var found *repository.AuditEntry
	for i := range out.Audit {
		if out.Audit[i].Action == "tenant.create" {
			found = &out.Audit[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no tenant.create audit entry found: %+v", out.Audit)
	}
	if found.Target != "acme" || found.TenantID != "acme" {
		t.Fatalf("audit entry fields: %+v", *found)
	}

	// Limit is honoured.
	resp, body = req(t, "GET", s.URL+"/v1/admin/audit?limit=1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit limit: status=%d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("audit limit decode: %v", err)
	}
	if len(out.Audit) != 1 {
		t.Fatalf("audit limit: got %d entries, want 1", len(out.Audit))
	}
}
