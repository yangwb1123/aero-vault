package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
