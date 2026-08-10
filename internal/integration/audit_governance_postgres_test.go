//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
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
	// 0044 dialect parity: the cumulative-window anchor is set on the first
	// claim and survives the lease re-claim unchanged (CASE WHEN idempotent).
	if crashed[0].FirstAttemptAt.IsZero() || recovered[0].FirstAttemptAt.IsZero() {
		t.Fatalf("Postgres anchor lost: crashed=%v recovered=%v",
			crashed[0].FirstAttemptAt, recovered[0].FirstAttemptAt)
	}
	if !recovered[0].FirstAttemptAt.Equal(crashed[0].FirstAttemptAt) {
		t.Fatalf("Postgres anchor moved across lease re-claim: %v → %v",
			crashed[0].FirstAttemptAt, recovered[0].FirstAttemptAt)
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

// TestPostgresAuditGovernanceInsertEventRoundTrip closes the B3.3 Defect-B
// coverage gap: it is the only PG test that executes InsertEventWithGovernance
// (the design's sole SQL change — RETURNING id → id, created_at + flexTime
// canonicalization — lives in that method). Mirrors AC-1's file lifecycle with
// a zero-CreatedAt event (service/file.go emit leaves CreatedAt zero, so the
// DB default must apply). Runs under the PG gate via the TestPostgres prefix
// (.github/workflows/integration-pg.yml: -run 'TestPostgres|TestPg').
//
// Design-time assertions (deterministic-fact-ids §7, AC-1-PG): outboxID is
// 32-hex (store-authoritative ID) and occurred_at_ns equals the canonicalized
// origin created_at .UnixNano() (REQ-2: PG now() is µs precision; pre-design
// the caller's ns OccurredAt was stored verbatim, so this equality only holds
// post-design).
func TestPostgresAuditGovernanceInsertEventRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo, raw := freshRepo(t)
	store := repo.(repository.AuditGovernanceStore)
	tenant := "audit-pg-" + uuid.NewString()
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-1",
		[]repository.AuditGovernanceBindingState{{TenantID: tenant, State: repository.AuditGovernanceBindingActive}}); err != nil {
		t.Fatalf("apply governance binding: %v", err)
	}
	// Zero CreatedAt event — mirrors service emit (file.go:308).
	event := repository.Event{TenantID: tenant, Bucket: "default", Key: "pg-governance.txt",
		Type: repository.EventCreated, Payload: map[string]string{"size": "7", "backend": "local"}}
	fact := repository.AuditGovernanceFact{
		ID: uuid.NewString(), TenantID: tenant, FactKind: "file", Action: "file.created",
		TargetDigest: "hmac-sha256:target", OccurredAt: time.Now().UTC(),
	}
	id, err := store.InsertEventWithGovernance(ctx, event, fact)
	if err != nil || id <= 0 {
		t.Fatalf("insert event with governance: id=%d err=%v", id, err)
	}
	// Origin row landed with the DB-default created_at (event CreatedAt was zero).
	var created time.Time
	if err := raw.QueryRowContext(ctx,
		`SELECT created_at FROM object_events WHERE id=$1`, id).Scan(&created); err != nil {
		t.Fatalf("read origin created_at: %v", err)
	}
	if created.IsZero() {
		t.Fatal("DB default created_at not applied to zero-CreatedAt event")
	}
	// Exactly one outbox row, matching the origin tuple.
	var outboxID string
	var occurredNS int64
	if err := raw.QueryRowContext(ctx, `SELECT id, occurred_at_ns FROM audit_governance_outbox
WHERE origin_kind='file' AND origin_id=$1 AND tenant_id=$2`, id, tenant).Scan(&outboxID, &occurredNS); err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	if outboxID == "" || occurredNS <= 0 {
		t.Fatalf("outbox row incomplete: id=%q occurred_at_ns=%d", outboxID, occurredNS)
	}
	// REQ-3/AC-1-PG: store-authoritative deterministic ID — 32 lowercase hex.
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(outboxID) {
		t.Fatalf("outbox id %q not 32-hex (deterministic fact ID)", outboxID)
	}
	// REQ-2/AC-1-PG: occurred was canonicalized to the DB-default created_at
	// (RETURNING id, created_at → flexTime), so the stored ns equals the
	// origin row's created_at exactly (PG now() is µs precision).
	if occurredNS != created.UnixNano() {
		t.Fatalf("occurred_at_ns=%d != origin created_at .UnixNano()=%d (REQ-2 canonicalization)",
			occurredNS, created.UnixNano())
	}
	// Claim/complete round-trip: the wire ID is the opaque outbox ID.
	claimed, err := store.ClaimAuditGovernance(ctx, "owner-pg", "token-pg", 1, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if claimed[0].ID != outboxID || claimed[0].OriginID != id {
		t.Fatalf("claimed fact=%+v want outbox id=%q origin=%d", claimed[0], outboxID, id)
	}
	if err := store.CompleteAuditGovernance(ctx, claimed[0].ID, "owner-pg", "token-pg"); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// TestPostgresAuditGovernancePruneReenqueueSameID (GAP-2 / v2 design §5.2)
// closes the PG mirror gap: the full ListGaps → factFromGap-equivalent →
// Enqueue loop on live PG, proving prune→re-enqueue folds to the SAME
// byte-identical ID (receiver-side Duplicate). It is the only test exercising
// the PG listGovernanceEventGaps SQL (JOIN + flexTime µs parse + "file."
// action prefix) on the prune path. factFromGap itself is package-private and
// pinned on sqlite by TestDeterministicFactID_GapEqualsAtomic_*; the ID
// depends only on the six formula inputs, reconstructed here identically, and
// EnqueueAuditGovernance recomputes the ID store-authoritatively.
func TestPostgresAuditGovernancePruneReenqueueSameID(t *testing.T) {
	ctx := context.Background()
	repo, raw := freshRepo(t)
	store := repo.(repository.AuditGovernanceStore)
	tenant := "audit-pg-" + uuid.NewString()
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-1",
		[]repository.AuditGovernanceBindingState{{TenantID: tenant, State: repository.AuditGovernanceBindingActive}}); err != nil {
		t.Fatalf("apply governance binding: %v", err)
	}
	const sourceID = "aero-vault.test-pg" // fixed literal — the store recomputes from fact.SourceID
	event := repository.Event{TenantID: tenant, Bucket: "default", Key: "pg-prune.txt",
		Type: repository.EventCreated, Payload: map[string]string{"size": "7", "backend": "local"}}
	fact := repository.AuditGovernanceFact{
		SourceID: sourceID, TenantID: tenant, FactKind: "file", Action: "file.created",
		OccurredAt: time.Time{}, // zero — store canonicalizes to the PG DB-default created_at (µs)
	}
	originID, err := store.InsertEventWithGovernance(ctx, event, fact)
	if err != nil || originID <= 0 {
		t.Fatalf("insert event with governance: id=%d err=%v", originID, err)
	}
	// Pre-prune outbox row.
	var preID string
	var preNS int64
	if err := raw.QueryRowContext(ctx, `SELECT id, occurred_at_ns FROM audit_governance_outbox
WHERE origin_kind='file' AND origin_id=$1 AND tenant_id=$2`, originID, tenant).Scan(&preID, &preNS); err != nil {
		t.Fatalf("read pre-prune outbox row: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(preID) {
		t.Fatalf("pre-prune id %q not 32-hex", preID)
	}
	// Prune (the T-4 bypass: no delivered-origin tombstone is written).
	if _, err := raw.ExecContext(ctx,
		`DELETE FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=$1`, originID); err != nil {
		t.Fatalf("prune outbox row: %v", err)
	}
	// Full gap path on live PG — exercises listGovernanceEventGaps' JOIN,
	// flexTime µs parse and "file." action prefix.
	gaps, err := store.ListAuditGovernanceGaps(ctx, tenant, 10)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("gaps=%+v err=%v want=1", gaps, err)
	}
	if gaps[0].OriginKind != "file" || gaps[0].OriginID != originID ||
		gaps[0].Action != "file.created" || gaps[0].OccurredAt.UnixNano() != preNS {
		t.Fatalf("gap=%+v want kind=file origin=%d action=file.created occurred=%d", gaps[0], originID, preNS)
	}
	// factFromGap-equivalent reconstruction (same six ID inputs factFromGap
	// feeds DeterministicFactID; factFromGap itself is pinned on sqlite by
	// TestDeterministicFactID_GapEqualsAtomic_*).
	rebuilt := repository.AuditGovernanceFact{
		SourceID: sourceID, TenantID: tenant, OriginKind: "file", FactKind: "file",
		Action: gaps[0].Action, OriginID: gaps[0].OriginID, OccurredAt: gaps[0].OccurredAt,
	}
	inserted, err := store.EnqueueAuditGovernance(ctx, rebuilt)
	if err != nil || !inserted {
		t.Fatalf("re-enqueue: inserted=%v err=%v", inserted, err)
	}
	// Byte-identical ID, exactly one row, and the dedupe branch returns (false, nil).
	var postID string
	var count int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*), MAX(id) FROM audit_governance_outbox
WHERE origin_kind='file' AND origin_id=$1 AND tenant_id=$2`, originID, tenant).Scan(&count, &postID); err != nil {
		t.Fatalf("read re-enqueued row: %v", err)
	}
	if count != 1 || postID != preID {
		t.Fatalf("re-enqueued count=%d id=%q want count=1 id=%q", count, postID, preID)
	}
	if again, err := store.EnqueueAuditGovernance(ctx, rebuilt); err != nil || again {
		t.Fatalf("duplicate enqueue inserted=%v err=%v want (false,nil)", again, err)
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

// pgPendingIdxSeed replicates the D3 probe-harness seed (gen_pg.py) on the
// fresh public schema: 20 bindings (t19 draining), 50,000 delivered + 5,000
// failed history rows, and 300 pending rows with heavy available_at_ns ties
// (200 claimable in 20 batches x 10 sharing one available_at_ns, 50 future,
// 50 leased-live). ANALYZE after seeding is required — the plan assertions
// depend on statistics, not on the raw row counts.
const pgPendingIdxSeed = `
INSERT INTO audit_governance_bindings (tenant_id, state, revision, updated_at_ns)
SELECT 't' || lpad(x::text, 2, '0'), CASE WHEN x = 19 THEN 'draining' ELSE 'active' END, 1, 1750000000000000000
FROM generate_series(1, 20) x;

INSERT INTO audit_governance_outbox
 (id, tenant_id, origin_kind, origin_id, fact_kind, actor_digest, action,
  target_digest, request_id, detail_sha256, object_size_bytes, storage_backend,
  occurred_at_ns, attempts, available_at_ns, claim_owner, claim_token,
  lease_expires_at_ns, last_error, created_at_ns, delivered_at_ns, failed_at_ns)
SELECT 'h' || lpad(x::text, 5, '0'),
  't' || lpad((1 + (x % 20))::text, 2, '0'),
  CASE WHEN x % 2 = 0 THEN 'admin' ELSE 'file' END,
  x,
  CASE WHEN x % 3 = 0 THEN 'admin' WHEN x % 3 = 1 THEN 'security' ELSE 'file' END,
  '', 'probe.action', '', '', '', 0, '',
  1750000000000000000 - x * 3600000000000, 0, 1750000000000000000 - x * 3600000000000, '', '', 0, '',
  1750000000000000000 - x * 3600000000000 - 50000000000,
  CASE WHEN x <= 50000 THEN 1750000000000000000 - x * 1000000000::bigint ELSE 0 END,
  CASE WHEN x > 50000 THEN 1750000000000000000 - x * 3600000000000 ELSE 0 END
FROM generate_series(1, 55000) x;

INSERT INTO audit_governance_outbox
 (id, tenant_id, origin_kind, origin_id, fact_kind, actor_digest, action,
  target_digest, request_id, detail_sha256, object_size_bytes, storage_backend,
  occurred_at_ns, attempts, available_at_ns, claim_owner, claim_token,
  lease_expires_at_ns, last_error, created_at_ns, delivered_at_ns, failed_at_ns)
SELECT 'p' || lpad(x::text, 3, '0'),
  't' || lpad((1 + (x % 20))::text, 2, '0'),
  CASE WHEN x % 2 = 0 THEN 'admin' ELSE 'file' END,
  100000 + x, 'security', '', 'probe.pending', '', '', '', 0, '',
  1750000000000000000 - ((x - 1) / 10 + 1) * 3600000000000, 0,
  1750000000000000000 - ((x - 1) / 10 + 1) * 3600000000000, '', '',
  CASE WHEN x % 2 = 0 THEN 0 ELSE 1750000000000000000 - 100000000000 END, '',
  1750000000000000000 - ((x - 1) / 10 + 1) * 3600000000000 + (x % 10) * 100000000::bigint, 0, 0
FROM generate_series(1, 200) x;

INSERT INTO audit_governance_outbox
 (id, tenant_id, origin_kind, origin_id, fact_kind, actor_digest, action,
  target_digest, request_id, detail_sha256, object_size_bytes, storage_backend,
  occurred_at_ns, attempts, available_at_ns, claim_owner, claim_token,
  lease_expires_at_ns, last_error, created_at_ns, delivered_at_ns, failed_at_ns)
SELECT 'f' || lpad(x::text, 3, '0'),
  't' || lpad((1 + (x % 20))::text, 2, '0'),
  CASE WHEN x % 2 = 0 THEN 'admin' ELSE 'file' END,
  200000 + x, 'security', '', 'probe.future', '', '', '', 0, '',
  1750000000000000000 + x * 3600000000000, 0, 1750000000000000000 + x * 3600000000000, '', '', 0, '',
  1750000000000000000 + x * 3600000000000 - 50000000000, 0, 0
FROM generate_series(1, 50) x;

INSERT INTO audit_governance_outbox
 (id, tenant_id, origin_kind, origin_id, fact_kind, actor_digest, action,
  target_digest, request_id, detail_sha256, object_size_bytes, storage_backend,
  occurred_at_ns, attempts, available_at_ns, claim_owner, claim_token,
  lease_expires_at_ns, last_error, created_at_ns, delivered_at_ns, failed_at_ns)
SELECT 'l' || lpad(x::text, 3, '0'),
  't' || lpad((1 + (x % 20))::text, 2, '0'),
  CASE WHEN x % 2 = 0 THEN 'admin' ELSE 'file' END,
  300000 + x, 'security', '', 'probe.leased', '', 'probe-token', '', 0, '',
  1750000000000000000 - x * 3600000000000, 0, 1750000000000000000 - x * 3600000000000, 'probe-owner', 'probe-token',
  1750000000000000000 + 3600000000000, '', 1750000000000000000 - x * 3600000000000 - 50000000000, 0, 0
FROM generate_series(1, 50) x;
`

// assertPGPlan runs EXPLAIN (FORMAT TEXT) and fails unless the plan names
// want and contains no forbid (REQ-5.3b's index-use + no-Seq-Scan assertions).
func assertPGPlan(t *testing.T, raw *sql.DB, query, want, forbid string) {
	t.Helper()
	ctx := context.Background()
	rows, err := raw.QueryContext(ctx, "EXPLAIN (FORMAT TEXT) "+query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	text := plan.String()
	t.Logf("plan:\n%s", text)
	if !strings.Contains(text, want) {
		t.Errorf("plan does not use %s", want)
	}
	if strings.Contains(text, forbid) {
		t.Errorf("plan contains %s", forbid)
	}
}

func TestPostgresAuditGovernancePendingIndexPlans(t *testing.T) {
	ctx := context.Background()
	_, raw := freshRepo(t)
	if _, err := raw.ExecContext(ctx, pgPendingIdxSeed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := raw.ExecContext(ctx, "ANALYZE"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	claim := `SELECT o.id FROM audit_governance_outbox o
JOIN audit_governance_bindings b ON b.tenant_id=o.tenant_id
WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0 AND o.available_at_ns <= 1750000000000000000
AND o.lease_expires_at_ns <= 1750000000000000000
AND b.revision=1
ORDER BY o.available_at_ns,o.created_at_ns,o.id LIMIT 32`
	assertPGPlan(t, raw, claim, "audit_governance_pending_claim_idx", "Seq Scan on audit_governance_outbox")
	lag := `SELECT MIN(o.created_at_ns)
FROM audit_governance_outbox o JOIN audit_governance_bindings b ON b.tenant_id=o.tenant_id
WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`
	assertPGPlan(t, raw, lag, "audit_governance_pending_lag_idx", "Seq Scan on audit_governance_outbox")
}
