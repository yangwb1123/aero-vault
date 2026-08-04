package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestObjectDeleteCleansAccessLifecycleState(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	accessStore := repo.(access.Store)
	obj, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: "acme", Bucket: "default", Key: "docs/report.txt",
		StorageKey: "acme/default/docs/report.txt", Size: 6, ETag: "etag",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedObjectAccessState(t, ctx, accessStore, obj)

	if err := repo.SoftDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err != nil {
		t.Fatal(err)
	}
	assertCapabilitiesGone(t, ctx, accessStore)
	entries, err := accessStore.ListResourceACL(ctx, obj.TenantID, obj.Bucket, obj.Key, access.ResourceObject)
	if err != nil || len(entries) != 0 {
		t.Fatalf("soft delete left exact ACLs: entries=%+v err=%v", entries, err)
	}
	if err := repo.HardDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err != nil {
		t.Fatal(err)
	}
	entries, err = accessStore.ListResourceACL(ctx, obj.TenantID, obj.Bucket, obj.Key, access.ResourceObject)
	if err != nil || len(entries) != 0 {
		t.Fatalf("hard delete left ACLs: entries=%+v err=%v", entries, err)
	}
	markerSource, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		StorageKey: "acme/default/docs/report-v2.txt", Size: 7, ETag: "etag-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedObjectAccessState(t, ctx, accessStore, markerSource)
	if _, err := repo.InsertDeleteMarker(ctx, repository.Object{
		TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		Metadata: map[string]string{"_aero_delete_marker": "true"},
	}); err != nil {
		t.Fatal(err)
	}
	assertCapabilitiesGone(t, ctx, accessStore)
}

func seedObjectAccessState(t *testing.T, ctx context.Context, store access.Store, obj repository.Object) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.CreateShare(ctx, access.Share{
		ID: "share-1", TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		TokenHash: "hash-1", AllowPreview: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPublicAsset(ctx, access.PublicAsset{
		ID: "asset-1", TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		Slug: "docs/report.txt", PublishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutACLEntry(ctx, access.ACLEntry{
		ID: "acl-1", TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "reader", Action: access.ActionRead, Effect: access.EffectAllow, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertCapabilitiesGone(t *testing.T, ctx context.Context, store access.Store) {
	t.Helper()
	if _, err := store.GetShare(ctx, "acme", "share-1"); !errors.Is(err, access.ErrNotFound) {
		t.Fatalf("share lookup error=%v, want ErrNotFound", err)
	}
	if _, err := store.GetPublicAsset(ctx, "docs/report.txt"); !errors.Is(err, access.ErrNotFound) {
		t.Fatalf("asset lookup error=%v, want ErrNotFound", err)
	}
}

func TestDepartmentDeleteCleansDescendantACLs(t *testing.T) {
	ctx := context.Background()
	repo := openTenantTestRepo(t)
	store := repo.(access.Store)
	now := time.Now().UTC()
	departments := []access.Department{
		{ID: "root", TenantID: "acme", Name: "engineering", CreatedAt: now, UpdatedAt: now},
		{ID: "child", TenantID: "acme", ParentID: "root", Name: "platform", CreatedAt: now, UpdatedAt: now},
		{ID: "other", TenantID: "acme", Name: "finance", CreatedAt: now, UpdatedAt: now},
	}
	for _, department := range departments {
		if err := store.PutDepartment(ctx, department); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutDepartmentMember(ctx, access.DepartmentMember{
		TenantID: "acme", DepartmentID: "child", SubjectID: "alice", Role: "member", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []access.ACLEntry{
		departmentACL("root-acl", "root", now),
		departmentACL("child-acl", "child", now),
		departmentACL("other-acl", "other", now),
	} {
		if err := store.PutACLEntry(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteDepartment(ctx, "acme", "root"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"root", "child"} {
		if _, err := store.GetDepartment(ctx, "acme", id); !errors.Is(err, access.ErrNotFound) {
			t.Fatalf("department %q survived delete: %v", id, err)
		}
	}
	entries, err := store.ListResourceACL(ctx, "acme", "default", "docs/", access.ResourceFolder)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "other-acl" {
		t.Fatalf("descendant ACL cleanup left %+v, want only other-acl", entries)
	}
}

func departmentACL(id, department string, created time.Time) access.ACLEntry {
	return access.ACLEntry{
		ID: id, TenantID: "acme", Bucket: "default", Key: "docs/",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeDepartment,
		PrincipalID: department, Action: access.ActionRead, Effect: access.EffectAllow,
		CreatedAt: created,
	}
}
