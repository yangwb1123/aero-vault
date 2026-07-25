package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "legal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

func seedObject(t *testing.T, repo repository.Repository, tenant, bucket, key string) int64 {
	t.Helper()
	ctx := context.Background()
	obj, err := repo.UpsertObject(ctx, repository.Object{
		TenantID:   tenant,
		Bucket:     bucket,
		Key:        key,
		Backend:    "local",
		StorageKey: "sk/" + key,
		Size:       10,
		ETag:       "e1",
	})
	if err != nil {
		t.Fatalf("UpsertObject: %v", err)
	}
	return obj.ID
}

func TestLegalHold_PutGet(t *testing.T) {
	ctx := context.Background()
	repo := openRepo(t)

	oid := seedObject(t, repo, "default", "bucket", "doc.txt")

	// Initially no hold.
	_, err := repo.GetLegalHold(ctx, oid, "")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatal("expected ErrNotFound for non-existent hold")
	}

	// Create hold.
	hold := repository.LegalHold{
		ObjectID:   oid,
		TenantID:   "default",
		HoldReason: "litigation",
		CreatedBy:  "admin",
	}
	if err := repo.PutLegalHold(ctx, hold); err != nil {
		t.Fatalf("PutLegalHold: %v", err)
	}

	// Read back.
	got, err := repo.GetLegalHold(ctx, oid, "")
	if err != nil {
		t.Fatalf("GetLegalHold: %v", err)
	}
	if got.HoldReason != "litigation" {
		t.Errorf("HoldReason = %q, want %q", got.HoldReason, "litigation")
	}
}

func TestLegalHold_ObjectHasLegalHold(t *testing.T) {
	ctx := context.Background()
	repo := openRepo(t)

	oid := seedObject(t, repo, "default", "bucket", "secret.txt")

	has, err := repo.ObjectHasLegalHold(ctx, oid)
	if err != nil {
		t.Fatalf("ObjectHasLegalHold: %v", err)
	}
	if has {
		t.Fatal("expected no hold initially")
	}

	if err := repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: oid, TenantID: "default",
		HoldReason: "regulatory", CreatedBy: "compliance",
	}); err != nil {
		t.Fatalf("PutLegalHold: %v", err)
	}

	has, err = repo.ObjectHasLegalHold(ctx, oid)
	if err != nil {
		t.Fatalf("ObjectHasLegalHold after put: %v", err)
	}
	if !has {
		t.Fatal("expected hold after put")
	}
}

func TestLegalHold_Remove(t *testing.T) {
	ctx := context.Background()
	repo := openRepo(t)

	oid := seedObject(t, repo, "default", "bucket", "important.pdf")

	// Remove non-existent returns error.
	if err := repo.RemoveLegalHold(ctx, oid, ""); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: oid, TenantID: "default",
		HoldReason: "eDiscovery", CreatedBy: "legal",
	}); err != nil {
		t.Fatalf("PutLegalHold: %v", err)
	}
	if err := repo.RemoveLegalHold(ctx, oid, ""); err != nil {
		t.Fatalf("RemoveLegalHold: %v", err)
	}

	_, err := repo.GetLegalHold(ctx, oid, "")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatal("expected ErrNotFound after remove")
	}
}

func TestLegalHold_List(t *testing.T) {
	ctx := context.Background()
	repo := openRepo(t)

	oid := seedObject(t, repo, "default", "bucket", "multi.txt")

	// Initially empty.
	holds, err := repo.ListLegalHolds(ctx, oid)
	if err != nil {
		t.Fatalf("ListLegalHolds: %v", err)
	}
	if len(holds) != 0 {
		t.Fatalf("expected 0 holds, got %d", len(holds))
	}

	// Add two holds.
	if err := repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: oid, TenantID: "default",
		HoldReason: "reason1", CreatedBy: "user1",
	}); err != nil {
		t.Fatalf("PutLegalHold 1: %v", err)
	}
	if err := repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: oid, TenantID: "default",
		HoldReason: "reason2", CreatedBy: "user2",
	}); err != nil {
		t.Fatalf("PutLegalHold 2: %v", err)
	}

	holds, err = repo.ListLegalHolds(ctx, oid)
	if err != nil {
		t.Fatalf("ListLegalHolds after put: %v", err)
	}
	if len(holds) != 2 {
		t.Fatalf("expected 2 holds, got %d", len(holds))
	}
}

func TestLegalHold_ListObjectsOnHold(t *testing.T) {
	ctx := context.Background()
	repo := openRepo(t)

	oid1 := seedObject(t, repo, "tenant1", "bucket", "a.txt")
	oid2 := seedObject(t, repo, "tenant1", "bucket", "b.txt")
	_ = oid2

	if err := repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: oid1, TenantID: "tenant1",
		HoldReason: "audit", CreatedBy: "admin",
	}); err != nil {
		t.Fatalf("PutLegalHold: %v", err)
	}

	ids, err := repo.ListObjectsOnLegalHold(ctx, "tenant1", 100)
	if err != nil {
		t.Fatalf("ListObjectsOnLegalHold: %v", err)
	}
	if len(ids) != 1 || ids[0] != oid1 {
		t.Fatalf("expected [%d], got %v", oid1, ids)
	}

	ids2, err := repo.ListObjectsOnLegalHold(ctx, "tenant2", 100)
	if err != nil {
		t.Fatalf("ListObjectsOnLegalHold tenant2: %v", err)
	}
	if len(ids2) != 0 {
		t.Fatalf("expected empty for tenant2, got %v", ids2)
	}
}
