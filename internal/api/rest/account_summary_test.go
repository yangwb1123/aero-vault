package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestAccountSummaryReturnsVaultDatasetsAndAudit(t *testing.T) {
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(t.TempDir(), "summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateBucket(t.Context(), "acme", "images"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateBucket(t.Context(), "acme", "documents"); err != nil {
		t.Fatal(err)
	}
	handler := NewAdminHandler(nil, repo, nil)
	request := httptest.NewRequest(http.MethodGet,
		"/v1/internal/account-summary?account_id=account-7&dataset=aero-vault.buckets&dataset=aero-vault.usage", nil)
	request.Header.Set(mw.TenantHeader, "acme")
	request.Header.Set("X-Aero-Tenant-ID", "acme")
	request.Header.Set("X-Aero-Canonical-UID", "snaplink-user-7")
	request = request.WithContext(access.WithPrincipal(request.Context(), access.Principal{
		SubjectID: "aero-id-vault-source", TenantID: "acme", Kind: access.PrincipalService,
		Scopes: []string{"read"},
	}))
	recorder := httptest.NewRecorder()
	mw.Tenant(http.HandlerFunc(handler.AccountSummary)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response vaultAccountSummaryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Complete || response.SourceRegion != "local" || response.Version < 1 {
		t.Fatalf("response metadata = %+v", response)
	}
	if _, ok := response.Datasets["aero-vault.buckets"]; !ok {
		t.Fatalf("datasets = %#v", response.Datasets)
	}
	if _, ok := response.Datasets["aero-vault.tenants"]; ok {
		t.Fatalf("unrequested dataset returned: %#v", response.Datasets)
	}
	if len(response.Sources) != 1 || len(response.Memberships) != 1 {
		t.Fatalf("references = %#v/%#v", response.Sources, response.Memberships)
	}
	entries, err := repo.ListAudit(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Action != "integration.account_summary.read" ||
		entries[0].Actor != "aero-id-vault-source" || entries[0].Target != "snaplink-user-7" {
		t.Fatalf("audit entries = %#v", entries)
	}
}

func TestAccountSummaryRejectsHumanAndTenantOverride(t *testing.T) {
	handler := NewAdminHandler(nil, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/internal/account-summary?account_id=account-7", nil)
	request.Header.Set("X-Aero-Canonical-UID", "user-7")
	request = request.WithContext(access.WithPrincipal(request.Context(), access.Principal{
		SubjectID: "user-7", TenantID: "acme", Kind: access.PrincipalUser,
	}))
	recorder := httptest.NewRecorder()
	handler.AccountSummary(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("human status = %d, want 403", recorder.Code)
	}
	var forbidden errorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &forbidden); err != nil {
		t.Fatal(err)
	}
	if forbidden.Error.Code != "Forbidden" {
		t.Fatalf("forbidden body = %#v", forbidden)
	}

	principal := access.Principal{SubjectID: "source", TenantID: "acme", Kind: access.PrincipalService}
	request = httptest.NewRequest(http.MethodGet, "/v1/internal/account-summary?account_id=account-7", nil)
	request.Header.Set("X-Aero-Canonical-UID", "user-7")
	request.Header.Set("X-Aero-Tenant-ID", "other")
	request = request.WithContext(access.WithPrincipal(request.Context(), principal))
	if _, ok := parseVaultAccountSummaryRequest(request, principal); ok {
		t.Fatal("tenant override was accepted")
	}
}

func TestAccountSummaryRequiresRepository(t *testing.T) {
	handler := NewAdminHandler(nil, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/internal/account-summary?account_id=account-7", nil)
	request.Header.Set(mw.TenantHeader, "acme")
	request.Header.Set("X-Aero-Tenant-ID", "acme")
	request.Header.Set("X-Aero-Canonical-UID", "user-7")
	request = request.WithContext(access.WithPrincipal(request.Context(), access.Principal{
		SubjectID: "source", TenantID: "acme", Kind: access.PrincipalService,
	}))
	recorder := httptest.NewRecorder()
	mw.Tenant(http.HandlerFunc(handler.AccountSummary)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	var unavailable errorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &unavailable); err != nil {
		t.Fatal(err)
	}
	if unavailable.Error.Code != "Unavailable" {
		t.Fatalf("unavailable body = %#v", unavailable)
	}
}

func TestRequestedVaultAccountDatasetsAreBounded(t *testing.T) {
	all, ok := requestedVaultAccountDatasets(url.Values{})
	if !ok || len(all) != len(vaultAccountDatasets) {
		t.Fatalf("defaults = %#v, ok=%v", all, ok)
	}
	if _, ok := requestedVaultAccountDatasets(url.Values{"dataset": {"aero-vault.secret"}}); ok {
		t.Fatal("unsupported dataset was accepted")
	}
	if _, ok := requestedVaultAccountDatasets(url.Values{"dataset": {
		"aero-vault.tenants", "aero-vault.buckets", "aero-vault.usage", "aero-vault.usage",
	}}); ok {
		t.Fatal("over-limit dataset request was accepted")
	}
}

func TestAccountSummaryIsDocumentedInOpenAPI(t *testing.T) {
	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	if _, ok := spec.Paths["/v1/internal/account-summary"]; !ok {
		t.Fatal("OpenAPI is missing GET /v1/internal/account-summary")
	}
}
