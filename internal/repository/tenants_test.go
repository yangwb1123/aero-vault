package repository_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openTenantTestRepo(t *testing.T) repository.Repository {
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

// TestTenantBudgetOverride verifies the per-tenant daily AI budget round-trips and
// that the budget and storage-cap setters never clobber each other (they share the
// tenant_quotas row).
func TestTenantBudgetOverride(t *testing.T) {
	ctx := context.Background()
	repo := openTenantTestRepo(t)

	// Default: no override.
	q, err := repo.GetTenantQuota(ctx, "acme")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if q.DailyBudgetMicros != 0 {
		t.Fatalf("default budget should be 0, got %d", q.DailyBudgetMicros)
	}

	// Set storage caps, then a budget override — the override must not clobber caps.
	if err := repo.SetTenantQuota(ctx, "acme", 999, 7); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if err := repo.SetTenantBudgetMicros(ctx, "acme", 50_000_000); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	q, _ = repo.GetTenantQuota(ctx, "acme")
	if q.DailyBudgetMicros != 50_000_000 {
		t.Fatalf("want budget 50e6, got %d", q.DailyBudgetMicros)
	}
	if q.MaxBytes != 999 || q.MaxObjects != 7 {
		t.Fatalf("setting budget clobbered caps: %+v", q)
	}

	// Updating caps must not clobber the budget.
	if err := repo.SetTenantQuota(ctx, "acme", 1, 1); err != nil {
		t.Fatalf("set quota again: %v", err)
	}
	q, _ = repo.GetTenantQuota(ctx, "acme")
	if q.DailyBudgetMicros != 50_000_000 {
		t.Fatalf("setting quota clobbered budget: %d", q.DailyBudgetMicros)
	}

	// Clearing with 0 removes the override.
	if err := repo.SetTenantBudgetMicros(ctx, "acme", 0); err != nil {
		t.Fatalf("clear budget: %v", err)
	}
	q, _ = repo.GetTenantQuota(ctx, "acme")
	if q.DailyBudgetMicros != 0 {
		t.Fatalf("override should be cleared, got %d", q.DailyBudgetMicros)
	}
}

// TestTenantUpsertGetListDelete walks the full lifecycle: upsert → get → list →
// delete, checking found/affected reporting along the way.
func TestTenantUpsertGetListDelete(t *testing.T) {
	ctx := context.Background()
	repo := openTenantTestRepo(t)

	tr := repository.TenantRecord{
		TenantID:    "acme",
		DisplayName: "Acme Corp",
		Status:      "active",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}
	if err := repo.UpsertTenant(ctx, tr); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, found, err := repo.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatalf("get: found=false, want true")
	}
	if got != tr {
		t.Fatalf("round trip mismatch:\n got=%+v\nwant=%+v", got, tr)
	}

	// Unknown tenant → not found, no error.
	_, found, err = repo.GetTenant(ctx, "nope")
	if err != nil {
		t.Fatalf("get unknown: %v", err)
	}
	if found {
		t.Fatalf("get unknown: found=true, want false")
	}

	// List returns the row.
	list, err := repo.ListTenants(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0] != tr {
		t.Fatalf("list: got %+v, want [%+v]", list, tr)
	}

	// Delete reports true once, then false; row is gone.
	deleted, err := repo.DeleteTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatalf("delete: got false, want true")
	}
	_, found, _ = repo.GetTenant(ctx, "acme")
	if found {
		t.Fatalf("get after delete: found=true, want false")
	}
	deleted, err = repo.DeleteTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("delete again: %v", err)
	}
	if deleted {
		t.Fatalf("delete again: got true, want false")
	}
}

// TestTenantListEmptyNonNil checks an empty store yields a non-nil empty slice.
func TestTenantListEmptyNonNil(t *testing.T) {
	ctx := context.Background()
	repo := openTenantTestRepo(t)

	list, err := repo.ListTenants(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list == nil {
		t.Fatalf("list: got nil slice, want non-nil empty")
	}
	if len(list) != 0 {
		t.Fatalf("list: got %d, want 0", len(list))
	}
}

// TestTenantUpsertKeepsCreatedAtAndUpdatesStatus re-upserts an existing tenant
// with changed display_name/status and verifies created_at is preserved while
// the mutable fields update.
func TestTenantUpsertKeepsCreatedAtAndUpdatesStatus(t *testing.T) {
	ctx := context.Background()
	repo := openTenantTestRepo(t)

	orig := repository.TenantRecord{
		TenantID:    "beta",
		DisplayName: "Beta",
		Status:      "active",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}
	if err := repo.UpsertTenant(ctx, orig); err != nil {
		t.Fatalf("upsert orig: %v", err)
	}

	updated := orig
	updated.DisplayName = "Beta Inc"
	updated.Status = "disabled"
	updated.CreatedAt = "2099-09-09T09:09:09Z" // should be ignored on conflict
	if err := repo.UpsertTenant(ctx, updated); err != nil {
		t.Fatalf("upsert updated: %v", err)
	}

	got, found, err := repo.GetTenant(ctx, "beta")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.DisplayName != "Beta Inc" {
		t.Fatalf("display_name=%q, want Beta Inc", got.DisplayName)
	}
	if got.Status != "disabled" {
		t.Fatalf("status=%q, want disabled", got.Status)
	}
	if got.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("created_at=%q, want original preserved", got.CreatedAt)
	}
}
