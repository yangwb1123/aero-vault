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

func TestBillingUsageTerminalFailedExcludedFromClaimAndDueScan(t *testing.T) {
	ctx := context.Background()
	_, store := openBillingTestStore(t)
	seededAt := time.Now().UTC()
	_, _, err := store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
		OperationID: "terminal-1", TenantID: "acme", Kind: "object_write",
		DeltaBytes: 9, OccurredAt: seededAt,
	})
	if err != nil {
		t.Fatalf("apply usage: %v", err)
	}
	oldest, ok, err := store.OldestPendingBillingUsage(ctx)
	if err != nil || !ok || oldest.Before(seededAt.Add(-time.Second)) {
		t.Fatalf("oldest pending: oldest=%v ok=%v err=%v", oldest, ok, err)
	}
	facts, err := store.ClaimBillingUsage(ctx, "worker", 10, time.Minute)
	if err != nil || len(facts) != 1 {
		t.Fatalf("claim usage: len=%d err=%v", len(facts), err)
	}
	if _, ok, err := store.OldestPendingBillingUsage(ctx); err != nil || !ok {
		t.Fatalf("inflight fact excluded from age probe: ok=%v err=%v", ok, err)
	}
	if err := store.RetryBillingUsage(ctx, facts[0].ID, facts[0].ClaimOwner,
		"permanent", time.Now().Add(-time.Minute), facts[0].Attempts); err != nil {
		t.Fatalf("terminalize billing fact: %v", err)
	}
	if _, ok, err := store.OldestPendingBillingUsage(ctx); err != nil || ok {
		t.Fatalf("terminal fact remained in age probe: ok=%v err=%v", ok, err)
	}
	if again, err := store.ClaimBillingUsage(ctx, "worker-2", 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("terminal billing fact was claimable: facts=%v err=%v", again, err)
	}

	_, _, err = store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
		OperationID: "cap-boundary", TenantID: "acme", Kind: "object_write",
		DeltaBytes: 7, OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("apply cap-boundary usage: %v", err)
	}
	first, err := store.ClaimBillingUsage(ctx, "worker-3", 10, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("first cap-boundary claim: len=%d err=%v", len(first), err)
	}
	if err := store.RetryBillingUsage(ctx, first[0].ID, first[0].ClaimOwner,
		"transient", time.Now().Add(-time.Minute), 2); err != nil {
		t.Fatalf("retry below cap: %v", err)
	}
	second, err := store.ClaimBillingUsage(ctx, "worker-4", 10, time.Minute)
	if err != nil || len(second) != 1 || second[0].Attempts != 2 {
		t.Fatalf("second cap-boundary claim: facts=%v err=%v", second, err)
	}
	if err := store.RetryBillingUsage(ctx, second[0].ID, second[0].ClaimOwner,
		"transient", time.Now().Add(-time.Minute), 2); err != nil {
		t.Fatalf("retry at cap: %v", err)
	}
	if final, err := store.ClaimBillingUsage(ctx, "worker-5", 10, time.Minute); err != nil || len(final) != 0 {
		t.Fatalf("fact at cap remained claimable: facts=%v err=%v", final, err)
	}
}
