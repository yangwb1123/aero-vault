//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestPostgresAuditGovernanceConcurrentClaimsAndLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	repoOne, rawDB := freshRepo(t)
	repoTwo, err := repository.Open(ctx, "postgres", pgDSN())
	if err != nil {
		t.Fatalf("open second replica: %v", err)
	}
	defer repoTwo.Close()
	storeOne := repoOne.(repository.AuditGovernanceStore)
	storeTwo := repoTwo.(repository.AuditGovernanceStore)
	tenant := "audit-pg-" + uuid.NewString()
	bindings := []repository.AuditGovernanceBindingState{{
		TenantID: tenant, State: repository.AuditGovernanceBindingActive,
	}}
	applyBindingsConcurrently(t, storeOne, storeTwo, bindings)
	seedGovernanceFacts(t, storeOne, tenant, 12)

	locked := lockGovernanceRows(t, rawDB, tenant, 4)
	claimed, err := storeTwo.ClaimAuditGovernance(ctx, "worker-b", "token-b", 1, 20, time.Minute)
	if err != nil || len(claimed) != 8 {
		t.Fatalf("SKIP LOCKED claim=%d want=8 err=%v", len(claimed), err)
	}
	completeGovernanceFacts(t, storeTwo, claimed, "worker-b", "token-b")
	if err := locked.Rollback(); err != nil {
		t.Fatalf("release row locks: %v", err)
	}
	remaining, err := storeOne.ClaimAuditGovernance(ctx, "worker-a", "token-a", 1, 20, time.Minute)
	if err != nil || len(remaining) != 4 {
		t.Fatalf("post-lock claim=%d want=4 err=%v", len(remaining), err)
	}
	assertDistinctGovernanceClaims(t, claimed, remaining)
	completeGovernanceFacts(t, storeOne, remaining, "worker-a", "token-a")

	seedGovernanceFacts(t, storeOne, tenant, 1)
	crashed, err := storeOne.ClaimAuditGovernance(ctx, "crashed", "stale", 1, 1, 150*time.Millisecond)
	if err != nil || len(crashed) != 1 {
		t.Fatalf("crash claim=%+v err=%v", crashed, err)
	}
	if facts, err := storeTwo.ClaimAuditGovernance(
		ctx, "recovery", "fresh", 1, 1, time.Second,
	); err != nil || len(facts) != 0 {
		t.Fatalf("live lease reclaimed early facts=%+v err=%v", facts, err)
	}
	time.Sleep(200 * time.Millisecond)
	recovered, err := storeTwo.ClaimAuditGovernance(ctx, "recovery", "fresh", 1, 1, time.Second)
	if err != nil || len(recovered) != 1 || recovered[0].Attempts != 2 {
		t.Fatalf("expired lease recovery=%+v err=%v", recovered, err)
	}
	if err := storeOne.CompleteAuditGovernance(ctx, crashed[0].ID, "crashed", "stale"); err == nil {
		t.Fatal("stale replica completed a recovered claim")
	}
	completeGovernanceFacts(t, storeTwo, recovered, "recovery", "fresh")
	cleaned, err := storeTwo.CleanupDeliveredAuditGovernance(ctx, time.Now().Add(time.Hour), 100)
	if err != nil || cleaned != 13 {
		t.Fatalf("Postgres delivered cleanup=%d want=13 err=%v", cleaned, err)
	}
	if gaps, err := storeTwo.ListAuditGovernanceGaps(ctx, tenant, 100); err != nil || len(gaps) != 0 {
		t.Fatalf("cleanup rebuilt delivered origins gaps=%+v err=%v", gaps, err)
	}
	if inserted, err := storeTwo.EnqueueAuditGovernance(ctx, recovered[0]); err != nil || inserted {
		t.Fatalf("tombstoned Postgres origin inserted=%v err=%v", inserted, err)
	}
}

func applyBindingsConcurrently(
	t *testing.T, first, second repository.AuditGovernanceStore,
	bindings []repository.AuditGovernanceBindingState,
) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	for _, store := range []repository.AuditGovernanceStore{first, second} {
		workers.Add(1)
		go func(store repository.AuditGovernanceStore) {
			defer workers.Done()
			<-start
			errs <- store.ApplyAuditGovernanceBindings(
				context.Background(), 1, "shared-manifest-digest", bindings)
		}(store)
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent binding apply: %v", err)
		}
	}
	if err := second.ApplyAuditGovernanceBindings(
		context.Background(), 1, "rollback-drift", bindings,
	); !errors.Is(err, repository.ErrAuditGovernanceRevisionDrift) {
		t.Fatalf("same-revision drift error=%v", err)
	}
}

func seedGovernanceFacts(
	t *testing.T, store repository.AuditGovernanceStore, tenant string, count int,
) {
	t.Helper()
	for range count {
		fact := repository.AuditGovernanceFact{
			ID: uuid.NewString(), TenantID: tenant, FactKind: "security",
			Action: "tenant.status", OccurredAt: time.Now().UTC(),
		}
		entry := repository.AuditEntry{TenantID: tenant, Action: "tenant.status"}
		if err := store.RecordAuditWithGovernance(context.Background(), entry, fact); err != nil {
			t.Fatalf("record governance fact: %v", err)
		}
	}
}

func lockGovernanceRows(t *testing.T, db *sql.DB, tenant string, count int) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin row-lock transaction: %v", err)
	}
	rows, err := tx.QueryContext(context.Background(), `SELECT id
FROM audit_governance_outbox WHERE tenant_id=$1 AND delivered_at_ns=0
ORDER BY created_at_ns,id LIMIT $2 FOR UPDATE`, tenant, count)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("lock governance rows: %v", err)
	}
	locked := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			t.Fatalf("scan locked row: %v", err)
		}
		locked++
	}
	if err := rows.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("close locked rows: %v", err)
	}
	if locked != count {
		_ = tx.Rollback()
		t.Fatalf("locked=%d want=%d", locked, count)
	}
	return tx
}

func completeGovernanceFacts(
	t *testing.T, store repository.AuditGovernanceStore,
	facts []repository.AuditGovernanceFact, owner, token string,
) {
	t.Helper()
	for _, fact := range facts {
		if err := store.CompleteAuditGovernance(
			context.Background(), fact.ID, owner, token,
		); err != nil {
			t.Fatalf("complete governance fact: %v", err)
		}
	}
}

func assertDistinctGovernanceClaims(
	t *testing.T, first, second []repository.AuditGovernanceFact,
) {
	t.Helper()
	seen := make(map[string]struct{}, len(first))
	for _, fact := range first {
		seen[fact.ID] = struct{}{}
	}
	for _, fact := range second {
		if _, duplicate := seen[fact.ID]; duplicate {
			t.Fatalf("fact %s was claimed twice", fact.ID)
		}
	}
}
