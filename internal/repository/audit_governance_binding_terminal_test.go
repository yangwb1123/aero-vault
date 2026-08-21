package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openGovernanceGateStore(t *testing.T) (repository.Repository, repository.AuditGovernanceStore, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "governance-gates.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		repo.Close()
		t.Fatalf("migrate repository: %v", err)
	}
	store, ok := repo.(repository.AuditGovernanceStore)
	if !ok {
		repo.Close()
		t.Fatal("repository does not implement AuditGovernanceStore")
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-1",
		[]repository.AuditGovernanceBindingState{{
			TenantID: "acme", State: repository.AuditGovernanceBindingActive,
		}}); err != nil {
		repo.Close()
		t.Fatalf("apply governance binding: %v", err)
	}
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		repo.Close()
		t.Fatalf("open raw database: %v", err)
	}
	t.Cleanup(func() {
		_ = raw.Close()
		_ = repo.Close()
	})
	return repo, store, raw
}

func seedGovernanceGateFact(t *testing.T, store repository.AuditGovernanceStore, terminal bool) {
	t.Helper()
	ctx := context.Background()
	if err := store.RecordAuditWithGovernance(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "tenant.status"},
		governanceFact("acme", "security", "tenant.status")); err != nil {
		t.Fatalf("record governance fact: %v", err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim governance fact len=%d err=%v", len(claimed), err)
	}
	if terminal {
		if err := store.FailAuditGovernance(ctx, claimed[0].ID, "worker", "token", "permanent"); err != nil {
			t.Fatalf("fail governance fact: %v", err)
		}
	}
}

func TestAuditGovernanceTerminalFailureDoesNotBlockBindingOrDisable(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openGovernanceGateStore(t)
	seedGovernanceGateFact(t, store, true)

	if err := store.ApplyAuditGovernanceBindings(ctx, 2, "digest-2",
		[]repository.AuditGovernanceBindingState{{
			TenantID: "beta", State: repository.AuditGovernanceBindingActive,
		}}); err != nil {
		t.Fatalf("terminal failed row blocked binding replacement: %v", err)
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 3, "digest-3-empty", nil); err != nil {
		t.Fatalf("terminal failed row blocked empty binding set: %v", err)
	}
	safe, err := store.AuditGovernanceCanDisable(ctx)
	if err != nil {
		t.Fatalf("check disable safety: %v", err)
	}
	if !safe {
		t.Fatal("only terminal failed governance rows should permit disable")
	}
}

func TestAuditGovernancePendingRowStillBlocksBindingAndDisable(t *testing.T) {
	ctx := context.Background()
	_, store, raw := openGovernanceGateStore(t)
	seedGovernanceGateFact(t, store, false)

	err := store.ApplyAuditGovernanceBindings(ctx, 2, "digest-2-empty", nil)
	var backlog *repository.AuditGovernanceUnboundBacklogError
	if !errors.As(err, &backlog) {
		t.Fatalf("pending row binding removal error=%v, want unbound backlog", err)
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM audit_governance_bindings`); err != nil {
		t.Fatalf("remove binding for disable predicate probe: %v", err)
	}
	safe, err := store.AuditGovernanceCanDisable(ctx)
	if err != nil {
		t.Fatalf("check disable safety: %v", err)
	}
	if safe {
		t.Fatal("pending governance rows should block disable even without bindings")
	}
}
