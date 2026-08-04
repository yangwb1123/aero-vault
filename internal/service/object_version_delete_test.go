package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeleteVersionRemovesOnlyTargetAndPromotesPrevious(t *testing.T) {
	svc, repo := newTestSvc(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "", "", "versions.txt", strings.NewReader("one"), 3, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := svc.Put(ctx, "", "", "versions.txt", strings.NewReader("two-two"), 7, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v3, err := svc.Put(ctx, "", "", "versions.txt", strings.NewReader("three"), 5, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteVersion(ctx, "", "", "versions.txt", v2.VersionID); err != nil {
		t.Fatalf("delete historical version: %v", err)
	}
	if _, _, err := svc.GetVersion(ctx, "", "", "versions.txt", v2.VersionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted version lookup error = %v, want ErrNotFound", err)
	}
	assertObjectBody(t, svc, "versions.txt", "three")

	if err := svc.DeleteVersion(ctx, "", "", "versions.txt", v3.VersionID); err != nil {
		t.Fatalf("delete current version: %v", err)
	}
	assertObjectBody(t, svc, "versions.txt", "one")
	current, err := svc.Stat(ctx, "", "", "versions.txt")
	if err != nil {
		t.Fatal(err)
	}
	if current.VersionID != v1.VersionID {
		t.Fatalf("promoted version = %q, want %q", current.VersionID, v1.VersionID)
	}
	quota, err := repo.GetTenantQuota(ctx, DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	if quota.UsedBytes != 3 || quota.UsedObjects != 1 {
		t.Fatalf("usage after version deletes = %d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}
}
