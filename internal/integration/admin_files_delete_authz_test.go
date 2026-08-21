package integration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type namedAdminDeleteProvider struct {
	allow bool
	err   error
}

func (p namedAdminDeleteProvider) Authorize(_ context.Context, _ access.Principal, _ access.Action, _ access.Resource) (access.Decision, error) {
	if p.err != nil {
		return access.Decision{}, p.err
	}
	if p.allow {
		return access.Decision{Allowed: true, Reason: "named_allow"}, nil
	}
	return access.Decision{Reason: "named_deny"}, nil
}

func adminDeleteProviders() map[string]rest.AuthorizationProvider {
	return map[string]rest.AuthorizationProvider{
		"deny-all":     namedAdminDeleteProvider{},
		"allow-all":    namedAdminDeleteProvider{allow: true},
		"err-provider": namedAdminDeleteProvider{err: errors.New("pdp outage")},
	}
}

func TestAC4_AdminDeleteNamedProvider(t *testing.T) {
	providers := adminDeleteProviders()
	t.Run("deny and outage fail closed", func(t *testing.T) {
		for _, name := range []string{"deny-all", "err-provider"} {
			t.Run(name, func(t *testing.T) {
				h := startFullServerNamed(t, nil, "opsecret:*:admin", providers, name)
				obj := putObjectAs(t, h, "acme", "named/deny.txt")
				resp := adminDeleteRequest(t, h.ts.URL, "acme", "named/deny.txt")
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("status=%d body=%s, want 403", resp.StatusCode, body)
				}
				if strings.Contains(string(body), "pdp outage") {
					t.Fatal("provider error leaked in response")
				}
				if _, err := h.repo.GetObject(context.Background(), "acme", "default", obj.Key); err != nil {
					t.Fatalf("object after denied delete: %v", err)
				}
				if n := outboxCountFor(t, h.dsn, obj.ID, repository.EventTypeFileDeleted11); n != 0 {
					t.Fatalf("deleted facts after denied delete = %d", n)
				}
			})
		}
	})
	t.Run("name selects allow provider", func(t *testing.T) {
		h := startFullServerNamed(t, nil, "opsecret:*:admin", providers, "allow-all")
		obj := putObjectAs(t, h, "acme", "named/allow.txt")
		resp := adminDeleteRequest(t, h.ts.URL, "acme", "named/allow.txt")
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status=%d, want 204", resp.StatusCode)
		}
		if n := outboxCountFor(t, h.dsn, obj.ID, repository.EventTypeFileDeleted11); n != 1 {
			t.Fatalf("deleted facts = %d, want 1", n)
		}
	})
}

func TestAC4_AdminDeleteAuthOffStillRequiresPrincipal(t *testing.T) {
	providers := map[string]rest.AuthorizationProvider{"admin-matrix": rest.AdminMatrixProvider{}}
	h := startFullServerNamed(t, nil, "", providers, "admin-matrix", serviceShape{
		deleteFailOpen: true,
	})
	obj := putObjectAs(t, h, "acme", "auth-off.txt")
	resp, err := http.DefaultClient.Do(mustHTTPDelete(t, h.ts.URL+"/v1/admin/files/acme/auth-off.txt?hard=1", ""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("auth-off admin delete status=%d, want 403", resp.StatusCode)
	}
	if _, err := h.repo.GetObject(context.Background(), "acme", "default", obj.Key); err != nil {
		t.Fatalf("object after auth-off denial: %v", err)
	}

	// The same service shape permits the documented legacy service delete only
	// once the registry supplies an attributable operator principal.
	h2 := startFullServerNamed(t, nil, "opsecret:*:admin", providers, "admin-matrix", serviceShape{
		deleteFailOpen: true,
	})
	putObjectAs(t, h2, "acme", "auth-on.txt")
	resp = adminDeleteRequest(t, h2.ts.URL, "acme", "auth-on.txt")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("auth-on operator delete status=%d, want 204", resp.StatusCode)
	}
}

func TestAC5_AccessOffDataPlaneDeleteGate(t *testing.T) {
	providers := map[string]rest.AuthorizationProvider{"admin-matrix": rest.AdminMatrixProvider{}}
	h := startFullServerNamed(t, nil, "opsecret:*:admin", providers, "admin-matrix", serviceShape{})
	put := putObjectAs(t, h, "acme", "data-plane.txt")
	getReq, err := http.NewRequest(http.MethodGet, h.ts.URL+"/v1/files/data-plane.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	getReq.Header.Set("Authorization", "Bearer opsecret")
	getReq.Header.Set(middleware.TenantHeader, "acme")
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("access-off GET status=%d, want 200", getResp.StatusCode)
	}
	deleteReq, err := http.NewRequest(http.MethodDelete, h.ts.URL+"/v1/files/data-plane.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteReq.Header.Set("Authorization", "Bearer opsecret")
	deleteReq.Header.Set(middleware.TenantHeader, "acme")
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatal(err)
	}
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusForbidden {
		t.Fatalf("access-off ordinary DELETE status=%d, want 403", deleteResp.StatusCode)
	}
	if _, err := h.repo.GetObject(context.Background(), "acme", "default", put.Key); err != nil {
		t.Fatalf("object after access-off delete denial: %v", err)
	}
}

func adminDeleteRequest(t *testing.T, base, tenant, key string) *http.Response {
	t.Helper()
	request := mustHTTPDelete(t, base+"/v1/admin/files/"+tenant+"/"+key+"?hard=1", "Bearer opsecret")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustHTTPDelete(t *testing.T, url, token string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	return req
}
