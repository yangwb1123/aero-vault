package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestHardDeleteKeyRemovesEveryVersionAndAdjustsUsage(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, nil)
	if err := svc.SetBucketVersioning(ctx, "default", "versions", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "default", "versions", "file.txt", strings.NewReader("one"), 3, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := svc.Put(ctx, "default", "versions", "file.txt", strings.NewReader("two-two"), 7, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "default", "versions", "file.txt", false); err != nil {
		t.Fatal(err)
	}
	versions, err := repo.ListObjectVersions(ctx, "default", "versions", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	var deletedCurrent repository.Object
	for _, version := range versions {
		if !version.VersionTombstone {
			deletedCurrent = version
		}
	}
	if deletedCurrent.ID == 0 {
		t.Fatal("soft-deleted current version not found")
	}
	if err := hardDeleteKey(ctx, repo, store, nil, deletedCurrent, slog.Default()); err != nil {
		t.Fatal(err)
	}
	versions, err = repo.ListObjectVersions(ctx, "default", "versions", "file.txt")
	if err != nil || len(versions) != 0 {
		t.Fatalf("versions after purge=%+v err=%v", versions, err)
	}
	for _, key := range []string{v1.StorageKey, v2.StorageKey} {
		if _, err := store.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("storage key %q survived purge: %v", key, err)
		}
	}
	quota, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if quota.UsedBytes != 0 || quota.UsedObjects != 0 {
		t.Fatalf("usage after purge=%d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}
}
