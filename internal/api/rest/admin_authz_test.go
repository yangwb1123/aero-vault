package rest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/service"
)

type adminDeleteTestProvider struct {
	decision access.Decision
	err      error
	calls    *atomic.Int32
	panic    bool
}

func (p adminDeleteTestProvider) Authorize(_ context.Context, _ access.Principal, _ access.Action, _ access.Resource) (access.Decision, error) {
	if p.calls != nil {
		p.calls.Add(1)
	}
	if p.panic {
		panic("pdp panic sentinel")
	}
	return p.decision, p.err
}

type adminDeleteTimeoutProvider struct{}

func (adminDeleteTimeoutProvider) Authorize(ctx context.Context, _ access.Principal, _ access.Action, _ access.Resource) (access.Decision, error) {
	<-ctx.Done()
	return access.Decision{}, ctx.Err()
}

func TestAdminDeleteProviderFailuresFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		provider   AuthorizationProvider
		wantReason string
		wantSecret string
	}{
		{name: "absent", provider: nil, wantReason: "no authorization provider configured"},
		{name: "error", provider: adminDeleteTestProvider{err: errors.New("pdp outage")}, wantReason: "AccessDenied", wantSecret: "pdp outage"},
		{name: "panic", provider: adminDeleteTestProvider{panic: true}, wantReason: "AccessDenied", wantSecret: "pdp panic sentinel"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil, tc.provider)
			obj := env.putObject(t, "acme", "deny.txt")
			resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/deny.txt?hard=1", nil,
				map[string]string{"Authorization": "Bearer opsecret"})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status=%d body=%s, want 403", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), tc.wantReason) {
				t.Errorf("body=%s, want %q", body, tc.wantReason)
			}
			if tc.wantSecret != "" && strings.Contains(string(body), tc.wantSecret) {
				t.Errorf("provider detail leaked in body=%s", body)
			}
			if _, err := env.repo.GetObject(context.Background(), "acme", service.DefaultBucket, obj.Key); err != nil {
				t.Fatalf("object after denied delete: %v", err)
			}
			env.assertNoWriteSideEffects(t, obj)
		})
	}
}

func TestAdminDeleteProviderTimeoutFailsClosed(t *testing.T) {
	previous := adminDeleteAuthzTimeout
	adminDeleteAuthzTimeout = 25 * time.Millisecond
	t.Cleanup(func() { adminDeleteAuthzTimeout = previous })
	env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil, adminDeleteTimeoutProvider{})
	obj := env.putObject(t, "acme", "timeout.txt")
	started := time.Now()
	resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/timeout.txt?hard=1", nil,
		map[string]string{"Authorization": "Bearer opsecret"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", resp.StatusCode, body)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout gate took %s, want bounded return", elapsed)
	}
	if _, err := env.repo.GetObject(context.Background(), "acme", service.DefaultBucket, obj.Key); err != nil {
		t.Fatalf("object after timeout denial: %v", err)
	}
	env.assertNoWriteSideEffects(t, obj)
}

func TestAdminMatrixProviderPermissionMatrix(t *testing.T) {
	resource := access.Resource{TenantID: "acme", Bucket: "default", Key: "x", Kind: access.ResourceObject}
	tests := []struct {
		name    string
		p       access.Principal
		allowed bool
		reason  string
	}{
		{name: "operator", p: access.Principal{TenantID: "*", Kind: access.PrincipalUser}, allowed: true},
		{name: "admin_scope", p: access.Principal{TenantID: "acme", Kind: access.PrincipalUser, Scopes: []string{"admin"}}, allowed: true},
		{name: "tenant_admin", p: access.Principal{TenantID: "acme", Kind: access.PrincipalUser, Roles: []string{"vault.tenant_admin"}}, allowed: true},
		{name: "file_admin", p: access.Principal{TenantID: "acme", Kind: access.PrincipalUser, Roles: []string{"vault.file_admin"}}, allowed: true},
		{name: "member", p: access.Principal{TenantID: "acme", Kind: access.PrincipalUser, Roles: []string{"vault.member"}}, reason: "default_deny"},
		{name: "write_scope", p: access.Principal{TenantID: "acme", Kind: access.PrincipalUser, Scopes: []string{"write"}}, reason: "default_deny"},
		{name: "cross_tenant", p: access.Principal{TenantID: "other", Kind: access.PrincipalUser, Scopes: []string{"admin"}}, reason: "tenant_mismatch"},
		{name: "anonymous", p: access.Principal{TenantID: "acme", Kind: access.PrincipalAnonymous}, reason: "default_deny"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := (AdminMatrixProvider{}).Authorize(context.Background(), tc.p, access.ActionDelete, resource)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed != tc.allowed {
				t.Fatalf("decision=%+v, allowed=%v", decision, tc.allowed)
			}
			if tc.reason != "" && decision.Reason != tc.reason {
				t.Errorf("reason=%q, want %q", decision.Reason, tc.reason)
			}
		})
	}
}
