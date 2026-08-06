package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openBillingTestStore(t *testing.T) (repository.Repository, repository.BillingStore) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "billing.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, ok := repo.(repository.BillingStore)
	if !ok {
		t.Fatal("repository does not implement BillingStore")
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, store
}

func TestBillingProjectionPreservesUsageAndRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	repo, store := openBillingTestStore(t)
	if _, err := repo.AddTenantUsage(ctx, "acme", 75, 3); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	now := time.Now().UTC()
	accepted, err := store.ApplyBillingProjection(ctx, repository.BillingProjection{
		TenantID: "acme", Revision: 2, Active: true,
		Bytes:       repository.BillingLimit{Hard: 100},
		Objects:     repository.BillingLimit{Hard: 10},
		EffectiveAt: now.Add(-time.Minute), ProjectedAt: now,
	})
	if err != nil || !accepted {
		t.Fatalf("apply projection: accepted=%v err=%v", accepted, err)
	}
	quota, err := repo.GetTenantQuota(ctx, "acme")
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if quota.UsedBytes != 75 || quota.UsedObjects != 3 || quota.MaxBytes != 100 || quota.MaxObjects != 10 {
		t.Fatalf("projected quota changed usage or caps incorrectly: %+v", quota)
	}
	accepted, err = store.ApplyBillingProjection(ctx, repository.BillingProjection{
		TenantID: "acme", Revision: 1, Active: true,
		Bytes:       repository.BillingLimit{Hard: 999},
		Objects:     repository.BillingLimit{Hard: 999},
		EffectiveAt: now.Add(-time.Minute), ProjectedAt: now.Add(time.Minute),
	})
	if err != nil || accepted {
		t.Fatalf("stale projection: accepted=%v err=%v", accepted, err)
	}
	quota, _ = repo.GetTenantQuota(ctx, "acme")
	if quota.MaxBytes != 100 || quota.MaxObjects != 10 || quota.UsedBytes != 75 {
		t.Fatalf("stale projection changed quota: %+v", quota)
	}
}

func TestBillingUsageIsAtomicIdempotentAndClaimable(t *testing.T) {
	ctx := context.Background()
	_, store := openBillingTestStore(t)
	mutation := repository.BillingUsageMutation{
		OperationID: "op-1", TenantID: "acme", Kind: "object_write",
		DeltaBytes: 40, DeltaObjects: 1, OccurredAt: time.Now().UTC(),
	}
	quota, duplicate, err := store.ApplyBillingUsage(ctx, mutation)
	if err != nil || duplicate {
		t.Fatalf("apply usage: duplicate=%v err=%v", duplicate, err)
	}
	if quota.UsedBytes != 40 || quota.UsedObjects != 1 {
		t.Fatalf("usage not applied: %+v", quota)
	}
	quota, duplicate, err = store.ApplyBillingUsage(ctx, mutation)
	if err != nil || !duplicate || quota.UsedBytes != 40 || quota.UsedObjects != 1 {
		t.Fatalf("idempotent replay: quota=%+v duplicate=%v err=%v", quota, duplicate, err)
	}
	facts, err := store.ClaimBillingUsage(ctx, "worker-a", 10, time.Minute)
	if err != nil {
		t.Fatalf("claim facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("claimed %d facts, want 2", len(facts))
	}
	dimensions := map[string]int64{}
	for _, fact := range facts {
		dimensions[fact.Dimension] = fact.Quantity
		if err := store.CompleteBillingUsage(ctx, fact.ID, fact.ClaimOwner); err != nil {
			t.Fatalf("complete %q: %v", fact.ID, err)
		}
	}
	if dimensions[repository.BillingDimensionBytesAllocated] != 40 ||
		dimensions[repository.BillingDimensionObjectsCreated] != 1 {
		t.Fatalf("unexpected allocation facts: %#v", dimensions)
	}
	if pending, err := store.ClaimBillingUsage(ctx, "worker-b", 10, time.Minute); err != nil || len(pending) != 0 {
		t.Fatalf("delivered facts reclaimed: facts=%v err=%v", pending, err)
	}
}

func TestBillingDeletionUsesPositiveReclaimDimensions(t *testing.T) {
	ctx := context.Background()
	repo, store := openBillingTestStore(t)
	if _, err := repo.AddTenantUsage(ctx, "acme", 80, 4); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	quota, _, err := store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
		OperationID: "delete-1", TenantID: "acme", Kind: "object_delete",
		DeltaBytes: -30, DeltaObjects: -2, OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("apply delete: %v", err)
	}
	if quota.UsedBytes != 50 || quota.UsedObjects != 2 {
		t.Fatalf("delete did not decrement local gauge: %+v", quota)
	}
	facts, err := store.ClaimBillingUsage(ctx, "worker", 10, time.Minute)
	if err != nil || len(facts) != 2 {
		t.Fatalf("claim delete facts: len=%d err=%v", len(facts), err)
	}
	dimensions := map[string]int64{}
	for _, fact := range facts {
		dimensions[fact.Dimension] = fact.Quantity
	}
	if dimensions[repository.BillingDimensionBytesReclaimed] != 30 ||
		dimensions[repository.BillingDimensionObjectsDeleted] != 2 {
		t.Fatalf("unexpected reclaim facts: %#v", dimensions)
	}
}
