package repository_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openQuotaTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// TestSetTenantQuotaUpsertAdvancesUpdatedAt re-upserts a quota and checks that
// updated_at is always populated (NOT NULL) and advances on the conflicting
// update. The SQLite branch sets updated_at=excluded.updated_at, which resolves
// to the column DEFAULT (current timestamp) for the omitted column — this guards
// that contract against regressions.
func TestSetTenantQuotaUpsertAdvancesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	repo := openQuotaTestRepo(t)

	if err := repo.SetTenantQuota(ctx, "acme", 100, 10); err != nil {
		t.Fatalf("set quota (insert): %v", err)
	}
	first, err := repo.GetTenantQuota(ctx, "acme")
	if err != nil {
		t.Fatalf("get after insert: %v", err)
	}
	if first.UpdatedAt.IsZero() {
		t.Fatalf("updated_at is zero after insert; want a real timestamp")
	}

	// Re-upsert (conflict path) with new caps; updated_at must not go backwards
	// or become zero, and the caps must take effect.
	if err := repo.SetTenantQuota(ctx, "acme", 200, 20); err != nil {
		t.Fatalf("set quota (upsert): %v", err)
	}
	second, err := repo.GetTenantQuota(ctx, "acme")
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if second.UpdatedAt.IsZero() {
		t.Fatalf("updated_at is zero after upsert; want a real timestamp")
	}
	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("updated_at went backwards: first=%s second=%s", first.UpdatedAt, second.UpdatedAt)
	}
	if second.MaxBytes != 200 || second.MaxObjects != 20 {
		t.Fatalf("upsert did not apply new caps: %+v", second)
	}
}

// TestAddTenantUsageCreatesThenIncrements verifies the upsert-then-increment
// path: the first call creates the row, and repeated calls accumulate (the
// ON CONFLICT (tenant_id) DO NOTHING / INSERT OR IGNORE step must not error on
// an existing row). Negative deltas decrement.
func TestAddTenantUsageCreatesThenIncrements(t *testing.T) {
	ctx := context.Background()
	repo := openQuotaTestRepo(t)

	q, err := repo.AddTenantUsage(ctx, "acme", 500, 3)
	if err != nil {
		t.Fatalf("add usage (create): %v", err)
	}
	if q.UsedBytes != 500 || q.UsedObjects != 3 {
		t.Fatalf("after first add: %+v, want bytes=500 objects=3", q)
	}

	q, err = repo.AddTenantUsage(ctx, "acme", 250, 1)
	if err != nil {
		t.Fatalf("add usage (increment): %v", err)
	}
	if q.UsedBytes != 750 || q.UsedObjects != 4 {
		t.Fatalf("after second add: %+v, want bytes=750 objects=4", q)
	}

	// Deletions: negative deltas decrement the counters.
	q, err = repo.AddTenantUsage(ctx, "acme", -250, -1)
	if err != nil {
		t.Fatalf("add usage (decrement): %v", err)
	}
	if q.UsedBytes != 500 || q.UsedObjects != 3 {
		t.Fatalf("after decrement: %+v, want bytes=500 objects=3", q)
	}
	if q.UpdatedAt.IsZero() {
		t.Fatalf("updated_at is zero after AddTenantUsage")
	}

	q, err = repo.AddTenantUsage(ctx, "acme", -5_000, -30)
	if err != nil {
		t.Fatalf("add usage (clamp): %v", err)
	}
	if q.UsedBytes != 0 || q.UsedObjects != 0 {
		t.Fatalf("usage must not become negative: %+v", q)
	}
}
