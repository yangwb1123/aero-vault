package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
)

func TestSessionReturnsNormalizedPrincipal(t *testing.T) {
	h := NewHandler(nil, nil)
	principal := access.Principal{
		SubjectID: "user-7", TenantID: "acme", Kind: access.PrincipalUser,
		Roles: []string{"viewer", "admin", "viewer"}, Groups: []string{"ops", "engineering"},
		Scopes: []string{"write", "read", "read"},
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/session", nil)
	req.Header.Set(mw.TenantHeader, "acme")
	req = req.WithContext(access.WithPrincipal(req.Context(), principal))
	recorder := httptest.NewRecorder()

	mw.Tenant(http.HandlerFunc(h.Session)).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var got sessionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Authenticated || got.SubjectID != "user-7" || got.TenantID != "acme" || got.PrincipalKind != access.PrincipalUser {
		t.Fatalf("identity = %+v", got)
	}
	assertStrings(t, "roles", got.Roles, []string{"admin", "viewer"})
	assertStrings(t, "groups", got.Groups, []string{"engineering", "ops"})
	assertStrings(t, "scopes", got.Scopes, []string{"read", "write"})
}

func TestSessionWithoutPrincipalReturnsAnonymousDevelopmentSession(t *testing.T) {
	h := NewHandler(nil, nil)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/session", nil).WithContext(context.Background())

	h.Session(recorder, req)

	var got sessionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Authenticated || got.TenantID != "default" || got.PrincipalKind != access.PrincipalAnonymous {
		t.Fatalf("session = %+v", got)
	}
	assertStrings(t, "roles", got.Roles, []string{})
	assertStrings(t, "groups", got.Groups, []string{})
	assertStrings(t, "scopes", got.Scopes, []string{})
}

func TestSessionIsDocumentedInOpenAPI(t *testing.T) {
	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}
	if _, ok := spec.Paths["/v1/session"]; !ok {
		t.Fatal("OpenAPI is missing GET /v1/session")
	}
}

func assertStrings(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %#v, want %#v", name, got, want)
		}
	}
}
