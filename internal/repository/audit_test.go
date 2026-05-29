package repository_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openAuditTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// TestAuditRecordAndList records two entries and verifies they come back
// newest-first with their fields intact, and that created_at is auto-stamped
// when omitted.
func TestAuditRecordAndList(t *testing.T) {
	ctx := context.Background()
	repo := openAuditTestRepo(t)

	if err := repo.RecordAudit(ctx, repository.AuditEntry{
		Actor:    "admin",
		Action:   "tenant.create",
		Target:   "acme",
		TenantID: "acme",
		Detail:   "Acme Corp",
	}); err != nil {
		t.Fatalf("record 1: %v", err)
	}
	if err := repo.RecordAudit(ctx, repository.AuditEntry{
		Actor:    "admin",
		Action:   "tenant.delete",
		Target:   "acme",
		TenantID: "acme",
	}); err != nil {
		t.Fatalf("record 2: %v", err)
	}

	list, err := repo.ListAudit(ctx, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list: got %d entries, want 2", len(list))
	}
	// Newest first: the delete (recorded last) leads.
	if list[0].Action != "tenant.delete" {
		t.Fatalf("order: list[0].Action=%q, want tenant.delete", list[0].Action)
	}
	if list[1].Action != "tenant.create" {
		t.Fatalf("order: list[1].Action=%q, want tenant.create", list[1].Action)
	}
	// Fields intact on the create entry.
	c := list[1]
	if c.Actor != "admin" || c.Target != "acme" || c.TenantID != "acme" || c.Detail != "Acme Corp" {
		t.Fatalf("create fields: %+v", c)
	}
	if c.ID == 0 || c.CreatedAt == "" {
		t.Fatalf("create id/created_at not populated: %+v", c)
	}
}

// TestAuditListLimitAndEmpty checks the limit clamp and that an empty store
// yields a non-nil empty slice.
func TestAuditListLimitAndEmpty(t *testing.T) {
	ctx := context.Background()
	repo := openAuditTestRepo(t)

	empty, err := repo.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if empty == nil {
		t.Fatalf("list empty: got nil slice, want non-nil")
	}
	if len(empty) != 0 {
		t.Fatalf("list empty: got %d, want 0", len(empty))
	}

	for i := 0; i < 5; i++ {
		if err := repo.RecordAudit(ctx, repository.AuditEntry{Action: "key.add"}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	limited, err := repo.ListAudit(ctx, 3)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 3 {
		t.Fatalf("list limited: got %d, want 3 (limit respected)", len(limited))
	}
}
