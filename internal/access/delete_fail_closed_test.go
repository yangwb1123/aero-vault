package access_test

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
)

func TestDisabledManagerDeleteFailsClosed(t *testing.T) {
	_, store, ctx := testManager(t, access.DefaultDeny)
	manager, err := access.NewManager(store, access.Config{
		Enabled:       false,
		DefaultPolicy: access.DefaultDeny,
		ShareSecret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := access.Principal{SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser}
	resource := access.Resource{TenantID: "acme", Bucket: "default", Key: "x.txt", Kind: access.ResourceObject}

	decision, err := manager.Authorize(ctx, principal, access.ActionDelete, resource)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || decision.Reason != "access_control_disabled" {
		t.Fatalf("disabled delete decision = %+v; want denied access_control_disabled", decision)
	}

	decision, err = manager.Authorize(ctx, principal, access.ActionRead, resource)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed {
		t.Fatalf("disabled read decision = %+v; want allowed", decision)
	}

	system := access.SystemContext(ctx, "acme")
	decision, err = manager.Authorize(system, access.Principal{Kind: access.PrincipalSystem}, access.ActionDelete, resource)
	if err != nil || !decision.Allowed {
		t.Fatalf("system delete decision = %+v, err=%v; want allowed", decision, err)
	}
}

func TestEnabledManagerDeleteNeedsExplicitPermission(t *testing.T) {
	manager, store, ctx := testManager(t, access.DefaultTenant)
	resource := access.Resource{TenantID: "acme", Bucket: "default", Key: "x.txt", Kind: access.ResourceObject, OwnerID: "alice"}
	for name, principal := range map[string]access.Principal{
		"admin scope":       {SubjectID: "ops", TenantID: "acme", Kind: access.PrincipalUser, Scopes: []string{"admin"}},
		"owner":             {SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser},
		"tenant admin role": {SubjectID: "ops", TenantID: "acme", Kind: access.PrincipalUser, Roles: []string{"vault.tenant_admin"}},
	} {
		t.Run(name, func(t *testing.T) {
			decision, err := manager.Authorize(ctx, principal, access.ActionDelete, resource)
			if err != nil || decision.Allowed {
				t.Fatalf("delete decision=%+v err=%v; want denied", decision, err)
			}
		})
	}
	granted := access.Principal{
		SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser,
		Scopes: []string{access.PermissionVaultFileDelete},
	}
	decision, err := manager.Authorize(ctx, granted, access.ActionDelete, resource)
	if err != nil || !decision.Allowed {
		t.Fatalf("explicit permission decision=%+v err=%v; want allowed", decision, err)
	}
	roleGranted := granted
	roleGranted.Scopes = nil
	roleGranted.Roles = []string{access.PermissionVaultFileDelete}
	decision, err = manager.Authorize(ctx, roleGranted, access.ActionDelete, resource)
	if err != nil || !decision.Allowed {
		t.Fatalf("explicit role decision=%+v err=%v; want allowed", decision, err)
	}
	admin := access.Principal{SubjectID: "root", TenantID: "acme", Kind: access.PrincipalUser, Scopes: []string{"admin"}}
	_, err = manager.PutACL(ctxWithPrincipal(ctx, admin), access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "x.txt", ResourceKind: access.ResourceObject,
		PrincipalType: access.PrincipalTypeUser, PrincipalID: "bob", Action: access.ActionAll, Effect: access.EffectAllow,
	})
	if err != nil {
		t.Fatal(err)
	}
	allGrant := access.Principal{SubjectID: "bob", TenantID: "acme", Kind: access.PrincipalUser}
	decision, err = manager.Authorize(ctx, allGrant, access.ActionDelete, resource)
	if err != nil || decision.Allowed {
		t.Fatalf("ActionAll ACL decision=%+v err=%v; want denied", decision, err)
	}
	_ = store
}

func ctxWithPrincipal(ctx context.Context, principal access.Principal) context.Context {
	return access.WithPrincipal(ctx, principal)
}
