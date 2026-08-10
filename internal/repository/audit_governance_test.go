package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openGovernanceStore(t *testing.T) (repository.Repository, repository.AuditGovernanceStore) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "governance.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	store, ok := repo.(repository.AuditGovernanceStore)
	if !ok {
		t.Fatal("repository does not implement AuditGovernanceStore")
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-1",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: repository.AuditGovernanceBindingActive}}); err != nil {
		t.Fatalf("apply governance binding: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, store
}

func governanceFact(tenant, kind, action string) repository.AuditGovernanceFact {
	return repository.AuditGovernanceFact{
		ID: uuid.NewString(), TenantID: tenant, FactKind: kind, Action: action,
		TargetDigest: "hmac-sha256:target", OccurredAt: time.Now().UTC(),
	}
}

func TestAuditGovernanceAtomicCaptureAndClaimFencing(t *testing.T) {
	ctx := context.Background()
	_, store := openGovernanceStore(t)
	fact := governanceFact("acme", "security", "key.add")
	entry := repository.AuditEntry{TenantID: "acme", Actor: "operator", Action: "key.add", Target: "acme"}
	if err := store.RecordAuditWithGovernance(ctx, entry, fact); err != nil {
		t.Fatalf("record atomic audit: %v", err)
	}
	eventFact := governanceFact("acme", "file", "file.created")
	event := repository.Event{TenantID: "acme", Bucket: "default", Key: "private.txt",
		Type: repository.EventCreated, Payload: map[string]string{"size": "12", "backend": "local"}}
	if _, err := store.InsertEventWithGovernance(ctx, event, eventFact); err != nil {
		t.Fatalf("record atomic event: %v", err)
	}

	claimed, err := store.ClaimAuditGovernance(ctx, "worker-a", "claim-a", 1, 10, time.Minute)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim facts=%d err=%v", len(claimed), err)
	}
	second, err := store.ClaimAuditGovernance(ctx, "worker-b", "claim-b", 1, 10, time.Minute)
	if err != nil || len(second) != 0 {
		t.Fatalf("leased facts were reclaimed: %d err=%v", len(second), err)
	}
	if err := store.CompleteAuditGovernance(ctx, claimed[0].ID, "worker-a", "wrong-token"); err == nil {
		t.Fatal("wrong claim token completed a fact")
	}
	if err := store.CompleteAuditGovernance(ctx, claimed[0].ID, "worker-a", "claim-a"); err != nil {
		t.Fatalf("complete owned claim: %v", err)
	}
	if err := store.RetryAuditGovernance(ctx, claimed[1].ID, "worker-a", "claim-a", "temporary", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("retry owned claim: %v", err)
	}
	reclaimed, err := store.ClaimAuditGovernance(ctx, "worker-b", "claim-b", 1, 10, time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Attempts != 2 {
		t.Fatalf("reclaim=%+v err=%v", reclaimed, err)
	}
}

func TestAuditGovernanceAtomicFailureRollsBackLocalAudit(t *testing.T) {
	ctx := context.Background()
	repo, store := openGovernanceStore(t)
	invalid := governanceFact("acme", "invalid", "key.add")
	err := store.RecordAuditWithGovernance(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "key.add"}, invalid)
	if err == nil {
		t.Fatal("invalid outbox fact unexpectedly succeeded")
	}
	entries, err := repo.ListAudit(ctx, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed transaction left local audit entries=%d err=%v", len(entries), err)
	}
}

func TestEnqueueSameFactTwiceDeduped(t *testing.T) {
	// AC-4 strengthening (H1): pin the second enqueue's (false,nil) to the
	// ON CONFLICT (origin_kind,origin_id) branch. (false,nil) alone is
	// three-way ambiguous — ON CONFLICT dup / delivered-origins guard /
	// unbound binding — so gap-empty + row-count==1 assertions are required;
	// the binding is provably active and the origin is fresh, so only the
	// dedupe branch can produce (false,nil) here.
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "ac4.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	store := repo.(repository.AuditGovernanceStore)
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-1",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: repository.AuditGovernanceBindingActive}}); err != nil {
		t.Fatalf("apply governance binding: %v", err)
	}
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer raw.Close()
	count := func() int {
		t.Helper()
		var n int
		if err := raw.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM audit_governance_outbox WHERE tenant_id='acme'`).Scan(&n); err != nil {
			t.Fatalf("count outbox rows: %v", err)
		}
		return n
	}
	if err := repo.RecordAudit(ctx, repository.AuditEntry{TenantID: "acme", Action: "tenant.status"}); err != nil {
		t.Fatalf("record local audit: %v", err)
	}
	gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("gaps=%+v err=%v want=1", gaps, err)
	}
	fact := governanceFact("acme", "admin", gaps[0].Action)
	fact.OriginKind, fact.OriginID = gaps[0].OriginKind, gaps[0].OriginID
	inserted, err := store.EnqueueAuditGovernance(ctx, fact)
	if err != nil || !inserted {
		t.Fatalf("first enqueue: inserted=%v err=%v", inserted, err)
	}
	// Store-authoritative deterministic ID (REQ-3/AC-4): the row's ID is the
	// 32-hex formula output over the fact's final fields, replacing the
	// caller-set UUID — and is identical on any re-enqueue of the same origin.
	expected := repository.DeterministicFactID("", "acme", fact.Action,
		fact.OriginKind, fact.OriginID, fact.OccurredAt)
	var rowID string
	if err := raw.QueryRowContext(ctx,
		`SELECT id FROM audit_governance_outbox WHERE tenant_id='acme'`).Scan(&rowID); err != nil {
		t.Fatalf("read outbox id: %v", err)
	}
	if rowID != expected {
		t.Fatalf("outbox id %q != deterministic formula %q (store-authoritative overwrite)", rowID, expected)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(rowID) {
		t.Fatalf("outbox id %q not 32-hex", rowID)
	}
	gaps, err = store.ListAuditGovernanceGaps(ctx, "acme", 10)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("gap not closed after first enqueue: gaps=%+v err=%v", gaps, err)
	}
	if n := count(); n != 1 {
		t.Fatalf("outbox rows=%d want=1 after first enqueue", n)
	}
	inserted, err = store.EnqueueAuditGovernance(ctx, fact)
	if err != nil || inserted {
		t.Fatalf("duplicate enqueue: inserted=%v err=%v want (false,nil)", inserted, err)
	}
	if n := count(); n != 1 {
		t.Fatalf("outbox rows=%d want=1 after duplicate enqueue", n)
	}
}

func TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts(t *testing.T) {
	ctx := context.Background()
	repo, store := openGovernanceStore(t)
	if err := repo.RecordAudit(ctx, repository.AuditEntry{TenantID: "acme", Action: "tenant.status"}); err != nil {
		t.Fatalf("record local audit: %v", err)
	}
	if _, err := repo.InsertEvent(ctx, repository.Event{TenantID: "acme", Bucket: "default",
		Key: "secret/path", Type: repository.EventDeleted}); err != nil {
		t.Fatalf("record local event: %v", err)
	}
	gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10)
	if err != nil || len(gaps) != 2 {
		t.Fatalf("gaps=%+v err=%v", gaps, err)
	}
	for _, gap := range gaps {
		fact := governanceFact("acme", "admin", gap.Action)
		fact.OriginKind, fact.OriginID = gap.OriginKind, gap.OriginID
		if gap.OriginKind == repository.AuditOriginFile {
			fact.FactKind = "file"
		}
		inserted, err := store.EnqueueAuditGovernance(ctx, fact)
		if err != nil || !inserted {
			t.Fatalf("enqueue gap: inserted=%v err=%v", inserted, err)
		}
		inserted, err = store.EnqueueAuditGovernance(ctx, fact)
		if err != nil || inserted {
			t.Fatalf("duplicate gap: inserted=%v err=%v", inserted, err)
		}
	}
	gaps, err = store.ListAuditGovernanceGaps(ctx, "acme", 10)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("reconciled gaps=%+v err=%v", gaps, err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || !ok {
		t.Fatalf("oldest pending ok=%v err=%v", ok, err)
	}
}

func TestAuditGovernanceReconcileBatchIsFairAcrossOrigins(t *testing.T) {
	ctx := context.Background()
	repo, store := openGovernanceStore(t)
	for range 3 {
		if err := repo.RecordAudit(ctx, repository.AuditEntry{
			TenantID: "acme", Action: "tenant.status",
		}); err != nil {
			t.Fatalf("record local audit: %v", err)
		}
	}
	if _, err := repo.InsertEvent(ctx, repository.Event{TenantID: "acme", Bucket: "default",
		Key: "secret/path", Type: repository.EventDeleted}); err != nil {
		t.Fatalf("record local event: %v", err)
	}
	gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 2)
	if err != nil || len(gaps) != 2 {
		t.Fatalf("gaps=%+v err=%v", gaps, err)
	}
	if gaps[0].OriginKind != repository.AuditOriginAdmin ||
		gaps[1].OriginKind != repository.AuditOriginFile {
		t.Fatalf("unfair reconcile batch: %+v", gaps)
	}
}

func TestAuditGovernanceBindingDrainDeleteRestoreIsLossless(t *testing.T) {
	ctx := context.Background()
	_, store := openGovernanceStore(t)
	active := repository.AuditGovernanceBindingActive
	draining := repository.AuditGovernanceBindingDraining
	if err := store.ApplyAuditGovernanceBindings(ctx, 2, "digest-2",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: active},
			{TenantID: "beta", State: active}}); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"acme", "beta"} {
		fact := governanceFact(tenant, "security", "tenant.status")
		if err := store.RecordAuditWithGovernance(ctx,
			repository.AuditEntry{TenantID: tenant, Action: "tenant.status"}, fact); err != nil {
			t.Fatalf("capture tenant fact: %v", err)
		}
	}
	err := store.ApplyAuditGovernanceBindings(ctx, 3, "digest-3-remove",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: active}})
	var backlog *repository.AuditGovernanceUnboundBacklogError
	if !errors.As(err, &backlog) || strings.Contains(err.Error(), "beta") {
		t.Fatalf("unsafe removal error=%v", err)
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 3, "digest-3-drain",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: active},
			{TenantID: "beta", State: draining}}); err != nil {
		t.Fatalf("enter drain: %v", err)
	}
	if err := store.RecordAuditWithGovernance(ctx,
		repository.AuditEntry{TenantID: "beta", Action: "quota.set"},
		governanceFact("beta", "admin", "quota.set")); err != nil {
		t.Fatalf("local-only draining fact: %v", err)
	}
	drainingPending, err := store.HasPendingDrainingAuditGovernance(ctx)
	if err != nil || !drainingPending {
		t.Fatalf("draining pending=%v err=%v", drainingPending, err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 3, 10, time.Minute)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim bound facts=%+v err=%v", claimed, err)
	}
	for _, fact := range claimed {
		if err := store.CompleteAuditGovernance(ctx, fact.ID, "worker", "token"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 4, "digest-4",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: active}}); err != nil {
		t.Fatalf("remove drained binding: %v", err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("unbound history polluted readiness ok=%v err=%v", ok, err)
	}
	gaps, err := store.ListAuditGovernanceGaps(ctx, "beta", 10)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("local draining history gaps=%+v err=%v", gaps, err)
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 5, "digest-5",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: active},
			{TenantID: "beta", State: active}}); err != nil {
		t.Fatalf("restore binding: %v", err)
	}
	restored := governanceFact("beta", "admin", gaps[0].Action)
	restored.OriginKind, restored.OriginID = gaps[0].OriginKind, gaps[0].OriginID
	if inserted, err := store.EnqueueAuditGovernance(ctx, restored); err != nil || !inserted {
		t.Fatalf("restore historical fact inserted=%v err=%v", inserted, err)
	}
}

func TestAuditGovernanceRevisionPersistsAndRejectsDrift(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "revision.db")
	open := func() (repository.Repository, repository.AuditGovernanceStore) {
		repo, err := repository.Open(ctx, "sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return repo, repo.(repository.AuditGovernanceStore)
	}
	binding := []repository.AuditGovernanceBindingState{{
		TenantID: "acme", State: repository.AuditGovernanceBindingActive}}
	repo, store := open()
	if err := store.ApplyAuditGovernanceBindings(ctx, 7, "stable-digest", binding); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 7, "stable-digest", binding); err != nil {
		t.Fatalf("same revision was not idempotent: %v", err)
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 7, "changed", binding); !errors.Is(err, repository.ErrAuditGovernanceRevisionDrift) {
		t.Fatalf("same revision drift error=%v", err)
	}
	_ = repo.Close()
	repo, store = open()
	defer repo.Close()
	if err := store.ApplyAuditGovernanceBindings(ctx, 6, "older", binding); !errors.Is(err, repository.ErrAuditGovernanceRevisionRollback) {
		t.Fatalf("restored rollback error=%v", err)
	}
}

func TestAuditGovernanceDisableRequiresExplicitEmptyBindingRevision(t *testing.T) {
	ctx := context.Background()
	_, store := openGovernanceStore(t)
	if safe, err := store.AuditGovernanceCanDisable(ctx); err != nil || safe {
		t.Fatalf("active bindings disable safe=%v err=%v", safe, err)
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 2, "empty-digest", nil); err != nil {
		t.Fatalf("apply empty desired bindings: %v", err)
	}
	if safe, err := store.AuditGovernanceCanDisable(ctx); err != nil || !safe {
		t.Fatalf("empty drained bindings disable safe=%v err=%v", safe, err)
	}
}

func TestAuditGovernanceSupersededReplicaCannotClaimNewRevision(t *testing.T) {
	ctx := context.Background()
	_, store := openGovernanceStore(t)
	if err := store.RecordAuditWithGovernance(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "tenant.status"},
		governanceFact("acme", "security", "tenant.status")); err != nil {
		t.Fatal(err)
	}
	binding := []repository.AuditGovernanceBindingState{{
		TenantID: "acme", State: repository.AuditGovernanceBindingActive,
	}}
	if err := store.ApplyAuditGovernanceBindings(ctx, 2, "rotated-digest", binding); err != nil {
		t.Fatal(err)
	}
	old, err := store.ClaimAuditGovernance(ctx, "old-replica", "old-token", 1, 1, time.Minute)
	if err != nil || len(old) != 0 {
		t.Fatalf("superseded claim=%+v err=%v", old, err)
	}
	current, err := store.ClaimAuditGovernance(ctx, "new-replica", "new-token", 2, 1, time.Minute)
	if err != nil || len(current) != 1 {
		t.Fatalf("current claim=%+v err=%v", current, err)
	}
}

func TestAuditGovernanceDeliveredCleanupLeavesOriginTombstone(t *testing.T) {
	ctx := context.Background()
	_, store := openGovernanceStore(t)
	fact := governanceFact("acme", "security", "key.add")
	if err := store.RecordAuditWithGovernance(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "key.add"}, fact); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if err := store.CompleteAuditGovernance(ctx, claimed[0].ID, "worker", "token"); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CleanupDeliveredAuditGovernance(ctx, time.Now().Add(-time.Hour), 10); err != nil || count != 0 {
		t.Fatalf("early cleanup count=%d err=%v", count, err)
	}
	if count, err := store.CleanupDeliveredAuditGovernance(ctx, time.Now().Add(time.Hour), 10); err != nil || count != 1 {
		t.Fatalf("cleanup count=%d err=%v", count, err)
	}
	if gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10); err != nil || len(gaps) != 0 {
		t.Fatalf("cleanup rebuilt gaps=%+v err=%v", gaps, err)
	}
	if inserted, err := store.EnqueueAuditGovernance(ctx, claimed[0]); err != nil || inserted {
		t.Fatalf("tombstoned origin inserted=%v err=%v", inserted, err)
	}
}

// Contract A terminal-with-retention: FailAuditGovernance is fenced by the
// claim identity, terminal rows are never re-claimed and never count as
// pending backlog, and CleanupFailedAuditGovernance prunes them only after
// the retention window — with no origin tombstone (a failed origin was never
// ledgered, so a fresh fact may enqueue after the prune).
func TestAuditGovernanceFirstAttemptAnchorPersists(t *testing.T) {
	// AC-3.1: first_attempt_at_ns is 0 on enqueue, set exactly once on the
	// FIRST claim (CASE WHEN inside the fenced claim UPDATE), and never reset
	// by retry, ack-lost re-claim, lease re-claim, or fail — idempotency of
	// the cumulative-window anchor across every re-claim path. Raw-SQL probes
	// pin the column itself; the claim read-back parity pins RETURNING.
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "anchor.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	store := repo.(repository.AuditGovernanceStore)
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-anchor",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: repository.AuditGovernanceBindingActive}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAuditWithGovernance(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "key.add"},
		governanceFact("acme", "security", "key.add")); err != nil {
		t.Fatalf("record atomic audit: %v", err)
	}
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	firstAttempt := func() int64 {
		t.Helper()
		var value int64
		if err := raw.QueryRowContext(ctx,
			`SELECT first_attempt_at_ns FROM audit_governance_outbox WHERE tenant_id='acme'`,
		).Scan(&value); err != nil {
			t.Fatalf("read first_attempt_at_ns: %v", err)
		}
		return value
	}
	if got := firstAttempt(); got != 0 {
		t.Fatalf("anchor before first claim=%d want 0 (safe default)", got)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim len=%d err=%v", len(claimed), err)
	}
	anchorNs := firstAttempt()
	if anchorNs == 0 {
		t.Fatal("first claim did not set the anchor")
	}
	if !claimed[0].FirstAttemptAt.Equal(time.Unix(0, anchorNs)) {
		t.Fatalf("claim read-back %v != column %d", claimed[0].FirstAttemptAt, anchorNs)
	}
	// Retry reschedules but never resets the anchor.
	if err := store.RetryAuditGovernance(ctx, claimed[0].ID, "worker", "token",
		"temporary", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("retry owned claim: %v", err)
	}
	if got := firstAttempt(); got != anchorNs {
		t.Fatalf("anchor moved by retry: %d → %d", anchorNs, got)
	}
	// Ack-lost / retry-path re-claim: anchor idempotent, attempts advance.
	reclaimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 1, time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Attempts != 2 {
		t.Fatalf("re-claim len=%d attempts=%d err=%v", len(reclaimed), reclaimed[0].Attempts, err)
	}
	if got := firstAttempt(); got != anchorNs {
		t.Fatalf("anchor moved by re-claim: %d → %d", anchorNs, got)
	}
	// Lease re-claim (crash path): short lease, let it expire, claim again.
	if err := store.RetryAuditGovernance(ctx, reclaimed[0].ID, "worker", "token",
		"temporary", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("release claim: %v", err)
	}
	expiring, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 1, 150*time.Millisecond)
	if err != nil || len(expiring) != 1 {
		t.Fatalf("expiring claim len=%d err=%v", len(expiring), err)
	}
	time.Sleep(200 * time.Millisecond) // lease (150ms) expires
	recovered, err := store.ClaimAuditGovernance(ctx, "recovery", "fresh", 1, 1, time.Minute)
	if err != nil || len(recovered) != 1 || recovered[0].Attempts != 4 {
		t.Fatalf("lease re-claim len=%d attempts=%d err=%v", len(recovered), recovered[0].Attempts, err)
	}
	if got := firstAttempt(); got != anchorNs {
		t.Fatalf("anchor moved by lease re-claim: %d → %d", anchorNs, got)
	}
	if !recovered[0].FirstAttemptAt.Equal(time.Unix(0, anchorNs)) {
		t.Fatalf("lease re-claim read-back %v != column %d", recovered[0].FirstAttemptAt, anchorNs)
	}
	// Terminal fail retains the anchor (diagnosis value of the row keeps it).
	if err := store.FailAuditGovernance(ctx, recovered[0].ID, "recovery", "fresh", "probe"); err != nil {
		t.Fatalf("fail fact: %v", err)
	}
	if got := firstAttempt(); got != anchorNs {
		t.Fatalf("anchor moved by fail: %d → %d", anchorNs, got)
	}
}

func TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned(t *testing.T) {
	ctx := context.Background()
	_, store := openGovernanceStore(t)
	fact := governanceFact("acme", "security", "key.add")
	if err := store.RecordAuditWithGovernance(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "key.add"}, fact); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	// Fencing: a stale owner/token cannot fail the fact.
	if err := store.FailAuditGovernance(ctx, claimed[0].ID, "stale-owner", "stale-token", "conflict"); err == nil {
		t.Fatal("stale claim identity failed the fact")
	}
	if err := store.FailAuditGovernance(ctx, claimed[0].ID, "worker", "token", "conflict:true"); err != nil {
		t.Fatalf("fail fact: %v", err)
	}
	// Terminal: never re-claimed, never pending.
	if again, err := store.ClaimAuditGovernance(ctx, "worker", "token-2", 1, 1, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("failed fact reclaimable: len=%d err=%v", len(again), err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("failed fact counts as pending ok=%v err=%v", ok, err)
	}
	// Retention: pruned only after the window.
	if count, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(-time.Hour), 10); err != nil || count != 0 {
		t.Fatalf("early failed cleanup count=%d err=%v", count, err)
	}
	if count, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(time.Hour), 10); err != nil || count != 1 {
		t.Fatalf("failed cleanup count=%d err=%v", count, err)
	}
	// No origin tombstone: the failed origin may be re-enqueued after prune.
	if inserted, err := store.EnqueueAuditGovernance(ctx, claimed[0]); err != nil || !inserted {
		t.Fatalf("failed origin not re-enqueueable after prune: inserted=%v err=%v", inserted, err)
	}
}
