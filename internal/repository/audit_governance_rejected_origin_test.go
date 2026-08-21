package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestAuditGovernancePermanentRejectionTombstonesOrigin(t *testing.T) {
	ctx := context.Background()
	_, store := openGovernanceStore(t)
	if err := store.RecordAuditWithGovernance(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "tenant.status"},
		governanceFact("acme", "security", "tenant.status")); err != nil {
		t.Fatalf("record governance fact: %v", err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim governance fact len=%d err=%v", len(claimed), err)
	}
	if err := store.RejectAuditGovernance(ctx, claimed[0].ID, "worker", "token", "permanent"); err != nil {
		t.Fatalf("reject governance fact: %v", err)
	}
	if gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10); err != nil || len(gaps) != 0 {
		t.Fatalf("rejected origin appeared as a live gap: gaps=%+v err=%v", gaps, err)
	}
	if count, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(time.Hour), 10); err != nil || count != 1 {
		t.Fatalf("cleanup rejected row count=%d err=%v", count, err)
	}
	if count, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(2*time.Hour), 10); err != nil || count != 0 {
		t.Fatalf("second cleanup removed another rejected row: count=%d err=%v", count, err)
	}
	if gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10); err != nil || len(gaps) != 0 {
		t.Fatalf("rejected origin resurrected after cleanup: gaps=%+v err=%v", gaps, err)
	}
	if inserted, err := store.EnqueueAuditGovernance(ctx, claimed[0]); err != nil || inserted {
		t.Fatalf("rejected origin re-enqueued: inserted=%v err=%v", inserted, err)
	}
}
