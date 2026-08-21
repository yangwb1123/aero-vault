package rest

import (
	"context"
	"encoding/json"
	"log/slog"
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

func newAdminScopeTestServer(t *testing.T) (*httptest.Server, *auth.Registry) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "scope.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	reg, err := auth.Parse("acme-admin:acme:admin,beta-admin:beta:admin,operator:*:admin")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	reg.WithJWT("jwt-test-secret")
	svc := service.NewFileService(store, repo, slog.Default()).WithDeleteFailOpen(true)
	v1 := NewRouter(svc, repo, nil, nil, nil, nil, reg, slog.Default(), false, nil, nil, 0, false)
	root := chi.NewRouter()
	root.Use(reg.Middleware())
	root.Use(mw.Tenant)
	root.Mount("/v1", v1)
	ts := httptest.NewServer(root)
	t.Cleanup(func() {
		ts.Close()
		_ = repo.Close()
	})
	return ts, reg
}

func adminRequestHeaders(token, tenant string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + token}
	if tenant != "" {
		h[mw.TenantHeader] = tenant
	}
	return h
}

func TestTenantAdminIsConfinedToItsTenant(t *testing.T) {
	s, reg := newAdminScopeTestServer(t)
	base := s.URL + "/v1/admin"
	acme := adminRequestHeaders("acme-admin", "acme")
	operator := adminRequestHeaders("operator", "")

	if resp, body := req(t, http.MethodPut, base+"/tenants/acme/quota", []byte(`{"max_bytes":100}`), acme); resp.StatusCode != http.StatusOK {
		t.Fatalf("same-tenant quota: status=%d body=%s", resp.StatusCode, body)
	}
	if resp, body := req(t, http.MethodPut, base+"/tenants/acme/quota", []byte(`{"max_bytes":-1}`), acme); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "max_bytes") {
		t.Fatalf("negative same-tenant quota: status=%d body=%s", resp.StatusCode, body)
	}
	if resp, body := req(t, http.MethodPut, base+"/tenants/beta/quota", []byte(`{"max_bytes":100}`), acme); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "tenant_mismatch") {
		t.Fatalf("cross-tenant quota: status=%d body=%s", resp.StatusCode, body)
	}

	if resp, body := req(t, http.MethodPost, base+"/keys", []byte(`{"token":"beta-delegated","tenant":"beta","scopes":["read"]}`), acme); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "tenant_mismatch") {
		t.Fatalf("cross-tenant key add: status=%d body=%s", resp.StatusCode, body)
	}
	if resp, body := req(t, http.MethodPost, base+"/keys", []byte(`{"token":"acme-delegated","tenant":"acme","scopes":["read"]}`), acme); resp.StatusCode != http.StatusCreated {
		t.Fatalf("same-tenant key add: status=%d body=%s", resp.StatusCode, body)
	}

	resp, body := req(t, http.MethodGet, base+"/keys", nil, acme)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tenant key list: status=%d body=%s", resp.StatusCode, body)
	}
	var listed struct {
		Keys []auth.Key `json:"keys"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode tenant key list: %v", err)
	}
	for _, key := range listed.Keys {
		if key.Tenant != "acme" {
			t.Fatalf("tenant key list leaked %q: %+v", key.Tenant, listed.Keys)
		}
	}

	if resp, body := req(t, http.MethodPost, base+"/jwt", []byte(`{"tenant":"beta","scopes":["read"],"ttl_seconds":60}`), acme); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "tenant_mismatch") {
		t.Fatalf("cross-tenant JWT: status=%d body=%s", resp.StatusCode, body)
	}
	if resp, body := req(t, http.MethodDelete, base+"/keys/beta-admin", nil, acme); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "tenant_mismatch") {
		t.Fatalf("cross-tenant key revoke: status=%d body=%s", resp.StatusCode, body)
	}
	if _, found, err := reg.TenantForKey(context.Background(), "beta-admin"); err != nil || !found {
		t.Fatalf("cross-tenant revoke removed beta key: found=%v err=%v", found, err)
	}

	for _, path := range []string{"/tenants", "/jobs", "/audit", "/config"} {
		if resp, body := req(t, http.MethodGet, base+path, nil, acme); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "operator admin scope required") {
			t.Fatalf("tenant admin global endpoint %s: status=%d body=%s", path, resp.StatusCode, body)
		}
	}
	if resp, body := req(t, http.MethodDelete, base+"/files/beta/missing.txt", nil, acme); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "tenant_mismatch") {
		t.Fatalf("cross-tenant file delete: status=%d body=%s", resp.StatusCode, body)
	}

	if resp, body := req(t, http.MethodGet, base+"/tenants", nil, operator); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator tenant list: status=%d body=%s", resp.StatusCode, body)
	}
	if resp, body := req(t, http.MethodPut, base+"/tenants/beta/quota", []byte(`{"max_bytes":200}`), operator); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator cross-tenant quota: status=%d body=%s", resp.StatusCode, body)
	}
}
