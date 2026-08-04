package access_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func testManager(t *testing.T, defaultPolicy string) (*access.Manager, context.Context) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	manager, err := access.NewManager(store, access.Config{
		Enabled: true, DefaultPolicy: defaultPolicy,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, ctx
}

func adminContext(ctx context.Context, tenant string) context.Context {
	return access.WithPrincipal(ctx, access.Principal{
		SubjectID: "admin", TenantID: tenant, Kind: access.PrincipalUser,
		Scopes: []string{"admin"},
	})
}

func TestDepartmentACLInheritance(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultDeny)
	admin := adminContext(ctx, "acme")
	department, err := manager.CreateDepartment(admin, access.Department{TenantID: "acme", Name: "engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PutDepartmentMember(admin, access.DepartmentMember{
		TenantID: "acme", DepartmentID: department.ID, SubjectID: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = manager.PutACL(admin, access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "engineering",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeDepartment,
		PrincipalID: department.ID, Action: access.ActionRead, Effect: access.EffectAllow, Inherit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	resource := access.Resource{TenantID: "acme", Bucket: "default", Key: "engineering/design.png", Kind: access.ResourceObject}
	alice := access.Principal{SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser}
	decision, err := manager.Authorize(ctx, alice, access.ActionDownload, resource)
	if err != nil || !decision.Allowed {
		t.Fatalf("department member denied: decision=%+v err=%v", decision, err)
	}
	bob := access.Principal{SubjectID: "bob", TenantID: "acme", Kind: access.PrincipalUser}
	decision, err = manager.Authorize(ctx, bob, access.ActionRead, resource)
	if err != nil || decision.Allowed {
		t.Fatalf("outsider should be denied: decision=%+v err=%v", decision, err)
	}
}

func TestExplicitDenyWinsAndOwnerCanRead(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultTenant)
	admin := adminContext(ctx, "acme")
	_, err := manager.PutACL(admin, access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "private.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectDeny,
	})
	if err != nil {
		t.Fatal(err)
	}
	resource := access.Resource{
		TenantID: "acme", Bucket: "default", Key: "private.txt",
		Kind: access.ResourceObject, OwnerID: "alice",
	}
	alice := access.Principal{
		SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser,
		Scopes: []string{"read", "write"},
	}
	decision, err := manager.Authorize(ctx, alice, access.ActionRead, resource)
	if err != nil || decision.Allowed || decision.Reason != "explicit_deny" {
		t.Fatalf("explicit deny did not win: decision=%+v err=%v", decision, err)
	}
	resource.Key = "owned.txt"
	decision, err = manager.Authorize(ctx, alice, access.ActionWrite, resource)
	if err != nil || !decision.Allowed || decision.Reason != "owner" {
		t.Fatalf("owner was not allowed: decision=%+v err=%v", decision, err)
	}
}

func TestPutACLIsIdempotentByNaturalKey(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultDeny)
	admin := adminContext(ctx, "acme")
	entry := access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "docs/", ResourceKind: access.ResourceFolder,
		PrincipalType: access.PrincipalTypeUser, PrincipalID: "alice",
		Action: access.ActionRead, Effect: access.EffectAllow, Inherit: true,
	}
	created, err := manager.PutACL(admin, entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.Effect = access.EffectDeny
	updated, err := manager.PutACL(admin, entry)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID {
		t.Fatalf("updated ACL id=%q, want %q", updated.ID, created.ID)
	}
	entries, err := manager.ListACL(admin, access.Resource{
		TenantID: "acme", Bucket: "default", Key: "docs/", Kind: access.ResourceFolder,
	})
	if err != nil || len(entries) != 1 || entries[0].Effect != access.EffectDeny {
		t.Fatalf("ACL entries=%+v err=%v", entries, err)
	}
}

func TestDepartmentACLRequiresExistingDepartment(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultDeny)
	_, err := manager.PutACL(adminContext(ctx, "acme"), access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "docs/", ResourceKind: access.ResourceFolder,
		PrincipalType: access.PrincipalTypeDepartment, PrincipalID: "missing",
		Action: access.ActionRead, Effect: access.EffectAllow, Inherit: true,
	})
	if !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("missing department error=%v, want ErrInvalidArgument", err)
	}
}

func TestSharePasswordAndUseLimit(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultDeny)
	admin := adminContext(ctx, "acme")
	share, token, err := manager.CreateShare(admin, access.CreateShareRequest{
		TenantID: "acme", Bucket: "default", Key: "photo.jpg", Password: "secret",
		AllowPreview: true, AllowDownload: true, MaxUses: 1,
	})
	if err != nil || token == "" || share.TokenHash != "" {
		// TokenHash is intentionally hidden only in JSON; the domain value remains populated.
		if err != nil || token == "" {
			t.Fatalf("create share: share=%+v token=%q err=%v", share, token, err)
		}
	}
	if _, _, err := manager.ResolveShare(ctx, token, "wrong", access.ActionDownload, true); !errors.Is(err, access.ErrBadPassword) {
		t.Fatalf("wrong password error=%v", err)
	}
	_, principal, err := manager.ResolveShare(ctx, token, "secret", access.ActionDownload, true)
	if err != nil || principal.Capability == nil {
		t.Fatalf("resolve share: principal=%+v err=%v", principal, err)
	}
	if _, _, err := manager.ResolveShare(ctx, token, "secret", access.ActionDownload, true); !errors.Is(err, access.ErrShareExpired) {
		t.Fatalf("exhausted share error=%v", err)
	}
}

func TestShareUseLimitIsAtomicUnderConcurrency(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultDeny)
	_, token, err := manager.CreateShare(adminContext(ctx, "acme"), access.CreateShareRequest{
		TenantID: "acme", Bucket: "default", Key: "limited.jpg",
		AllowPreview: true, MaxUses: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 12
	start := make(chan struct{})
	results := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, _, resolveErr := manager.ResolveShare(ctx, token, "", access.ActionPreview, true)
			results <- resolveErr
		}()
	}
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for resolveErr := range results {
		if resolveErr == nil {
			successes++
		} else if !errors.Is(resolveErr, access.ErrShareExpired) {
			t.Fatalf("concurrent resolve error=%v", resolveErr)
		}
	}
	if successes != 1 {
		t.Fatalf("successful resolves=%d, want exactly 1", successes)
	}
}

func TestPublicAssetLifecycle(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultDeny)
	admin := adminContext(ctx, "acme")
	asset, err := manager.PublishAsset(admin, access.PublicAsset{
		TenantID: "acme", Bucket: "default", Key: "blog/hero.jpg", Slug: "blog/hero.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, principal, err := manager.ResolvePublicAsset(ctx, asset.Slug)
	if err != nil || resolved.Key != asset.Key || principal.Capability == nil {
		t.Fatalf("resolve asset: asset=%+v principal=%+v err=%v", resolved, principal, err)
	}
	if err := manager.UnpublishAsset(admin, "acme", asset.Slug); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.ResolvePublicAsset(ctx, asset.Slug); !errors.Is(err, access.ErrNotFound) {
		t.Fatalf("unpublished asset error=%v", err)
	}
}

func TestExpiredShareRejected(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultDeny)
	_, token, err := manager.CreateShare(adminContext(ctx, "acme"), access.CreateShareRequest{
		TenantID: "acme", Bucket: "default", Key: "old.png", AllowPreview: true,
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.ResolveShare(ctx, token, "", access.ActionPreview, false); !errors.Is(err, access.ErrShareExpired) {
		t.Fatalf("expired share error=%v", err)
	}
}

func TestObjectOwnerCanRevokeCollaboratorCapabilities(t *testing.T) {
	manager, ctx := testManager(t, access.DefaultDeny)
	admin := adminContext(ctx, "acme")
	resource := access.Resource{
		TenantID: "acme", Bucket: "default", Key: "team/photo.jpg",
		Kind: access.ResourceObject, OwnerID: "owner",
	}
	for _, action := range []access.Action{access.ActionShare, access.ActionPublish} {
		_, err := manager.PutACL(admin, access.ACLEntry{
			TenantID: resource.TenantID, Bucket: resource.Bucket, Key: resource.Key,
			ResourceKind: resource.Kind, PrincipalType: access.PrincipalTypeUser,
			PrincipalID: "collaborator", Action: action, Effect: access.EffectAllow,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	collaborator := access.WithPrincipal(ctx, access.Principal{
		SubjectID: "collaborator", TenantID: "acme", Kind: access.PrincipalUser,
	})
	share, _, err := manager.CreateShare(collaborator, access.CreateShareRequest{
		TenantID: resource.TenantID, Bucket: resource.Bucket, Key: resource.Key,
		AllowPreview: true, OwnerID: resource.OwnerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	asset, err := manager.PublishAsset(collaborator, access.PublicAsset{
		TenantID: resource.TenantID, Bucket: resource.Bucket, Key: resource.Key,
		Slug: "team/photo.jpg", OwnerID: resource.OwnerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := access.WithPrincipal(ctx, access.Principal{
		SubjectID: resource.OwnerID, TenantID: "acme", Kind: access.PrincipalUser,
	})
	if err := manager.RevokeShare(owner, "acme", share.ID); err != nil {
		t.Fatalf("owner revoke share: %v", err)
	}
	if err := manager.UnpublishAsset(owner, "acme", asset.Slug); err != nil {
		t.Fatalf("owner unpublish asset: %v", err)
	}
}
