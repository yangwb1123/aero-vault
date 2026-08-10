package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// pendingProbeNow is the deterministic seed clock used by the D3 probe
// harness (docs/auto/runs/permanent-error-classification-terminal-within-1-3a34c930/
// artifacts/design-validate-d3/). All seeded rows are relative to it and the
// claim/lag probes bind it, so the plan assertions run against the exact
// verified configuration.
const pendingProbeNow = int64(1750000000000000000)

// pendingIdxSeed replays that harness's seed through the pinned modernc
// driver (v1.50.1 — SQLite 3.53.1): 20 bindings (t19 draining), 50,000
// delivered + 5,000 failed history rows, and 300 pending rows with heavy
// available_at_ns ties (200 claimable in 20 batches x 10 sharing one
// available_at_ns, 50 future, 50 leased-live).
//
// Seeding guidance (empirically verified, see the D3 validation report):
//   - pending-row available_at_ns TIES are the decisive planner lever (they
//     mirror a batch-flush pattern); "extra delivered rows" alone is a weak
//     lever (the strict CLI only flips at ~1M delivered rows).
//   - ANALYZE after seeding is REQUIRED: without sqlite_stat1 the planner
//     picks audit_governance_due_idx + a temp b-tree and the
//     pending_claim_idx assertion fails outright.
//   - plan CHOICE is SQLite-version-sensitive (3.53.1 vs 3.53.4 differ at
//     this seed); the committed test pins modernc v1.50.1, so it is stable.
//     The no-sort claim itself holds on all tested versions.
var pendingIdxSeed = []string{
	`INSERT INTO audit_governance_bindings (tenant_id, state, revision, updated_at_ns)
WITH RECURSIVE t(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM t WHERE x < 20)
SELECT printf('t%02d', x), CASE WHEN x = 19 THEN 'draining' ELSE 'active' END, 1,
1750000000000000000 FROM t;`,
	`WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < 55000)
INSERT INTO audit_governance_outbox
 (id, tenant_id, origin_kind, origin_id, fact_kind, actor_digest, action,
  target_digest, request_id, detail_sha256, object_size_bytes, storage_backend,
  occurred_at_ns, attempts, available_at_ns, claim_owner, claim_token,
  lease_expires_at_ns, last_error, created_at_ns, delivered_at_ns, failed_at_ns)
SELECT
  printf('h%05d', x),
  printf('t%02d', 1 + (x % 20)),
  CASE WHEN x % 2 = 0 THEN 'admin' ELSE 'file' END,
  x,
  CASE WHEN x % 3 = 0 THEN 'admin' WHEN x % 3 = 1 THEN 'security' ELSE 'file' END,
  '', 'probe.action', '', '', '', 0, '',
  1750000000000000000 - x * 3600000000000, 0,
  1750000000000000000 - x * 3600000000000, '', '', 0, '',
  1750000000000000000 - x * 3600000000000 - 50000000000,
  CASE WHEN x <= 50000 THEN 1750000000000000000 - x * 1000000000 ELSE 0 END,
  CASE WHEN x > 50000 THEN 1750000000000000000 - x * 3600000000000 ELSE 0 END
FROM cnt;`,
	`WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < 200)
INSERT INTO audit_governance_outbox
 (id, tenant_id, origin_kind, origin_id, fact_kind, actor_digest, action,
  target_digest, request_id, detail_sha256, object_size_bytes, storage_backend,
  occurred_at_ns, attempts, available_at_ns, claim_owner, claim_token,
  lease_expires_at_ns, last_error, created_at_ns, delivered_at_ns, failed_at_ns)
SELECT
  printf('p%03d', x),
  printf('t%02d', 1 + (x % 20)),
  CASE WHEN x % 2 = 0 THEN 'admin' ELSE 'file' END,
  100000 + x, 'security', '', 'probe.pending', '', '', '', 0, '',
  1750000000000000000 - ((x - 1) / 10 + 1) * 3600000000000, 0,
  1750000000000000000 - ((x - 1) / 10 + 1) * 3600000000000, '', '',
  CASE WHEN x % 2 = 0 THEN 0 ELSE 1750000000000000000 - 100000000000 END, '',
  1750000000000000000 - ((x - 1) / 10 + 1) * 3600000000000 + (x % 10) * 100000000,
  0, 0
FROM cnt;`,
	`WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < 50)
INSERT INTO audit_governance_outbox
 (id, tenant_id, origin_kind, origin_id, fact_kind, actor_digest, action,
  target_digest, request_id, detail_sha256, object_size_bytes, storage_backend,
  occurred_at_ns, attempts, available_at_ns, claim_owner, claim_token,
  lease_expires_at_ns, last_error, created_at_ns, delivered_at_ns, failed_at_ns)
SELECT
  printf('f%03d', x),
  printf('t%02d', 1 + (x % 20)),
  CASE WHEN x % 2 = 0 THEN 'admin' ELSE 'file' END,
  200000 + x, 'security', '', 'probe.future', '', '', '', 0, '',
  1750000000000000000 + x * 3600000000000, 0,
  1750000000000000000 + x * 3600000000000, '', '', 0, '',
  1750000000000000000 + x * 3600000000000 - 50000000000, 0, 0
FROM cnt;`,
	`WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < 50)
INSERT INTO audit_governance_outbox
 (id, tenant_id, origin_kind, origin_id, fact_kind, actor_digest, action,
  target_digest, request_id, detail_sha256, object_size_bytes, storage_backend,
  occurred_at_ns, attempts, available_at_ns, claim_owner, claim_token,
  lease_expires_at_ns, last_error, created_at_ns, delivered_at_ns, failed_at_ns)
SELECT
  printf('l%03d', x),
  printf('t%02d', 1 + (x % 20)),
  CASE WHEN x % 2 = 0 THEN 'admin' ELSE 'file' END,
  300000 + x, 'security', '', 'probe.leased', '', 'probe-token', '', 0, '',
  1750000000000000000 - x * 3600000000000, 0,
  1750000000000000000 - x * 3600000000000, 'probe-owner', 'probe-token',
  1750000000000000000 + 3600000000000, '',
  1750000000000000000 - x * 3600000000000 - 50000000000, 0, 0
FROM cnt;`,
}

func openPendingIdxStore(t *testing.T) (*sqlStore, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	repo, err := Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "pending-idx.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(*sqlStore)
	return store, store.db.DB
}

func seedPendingIdx(t *testing.T, raw *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range pendingIdxSeed {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// REQUIRED: without sqlite_stat1 the planner picks due_idx + temp b-tree
	// and the pending_claim_idx assertion fails (see pendingIdxSeed guidance).
	if _, err := raw.ExecContext(ctx, "ANALYZE"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
}

// assertPlan runs EXPLAIN QUERY PLAN (perf_probe_test.go pattern, upgraded
// from t.Log to t.Errorf — the D5.3 binding assertion) and fails unless at
// least one plan row names want (empty = not required) and no row contains
// any forbid string.
func assertPlan(t *testing.T, raw *sql.DB, query string, args []any, want string, forbids ...string) {
	t.Helper()
	ctx := context.Background()
	rows, err := raw.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := want == ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Logf("plan: %s", detail)
		if want != "" && strings.Contains(detail, want) {
			found = true
		}
		for _, forbid := range forbids {
			if strings.Contains(detail, forbid) {
				t.Errorf("plan for %q contains %q (full-table scan): %s", query, forbid, detail)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Errorf("plan for %q does not use %s", query, want)
	}
}

func TestAuditGovernancePendingIndexesServeClaimAndLagPlans(t *testing.T) {
	_, raw := openPendingIdxStore(t)
	seedPendingIdx(t, raw)
	// Claim path — the exact SQLite claim inner SELECT shape
	// (audit_governance_claim.go claimAuditGovernanceSQLite), bound-parameter
	// form (D5.3 probe pattern). The alias-qualified full-table scan reads
	// "SCAN o" in EXPLAIN QUERY PLAN output.
	claim := `SELECT o.id FROM audit_governance_outbox o
JOIN audit_governance_bindings b ON b.tenant_id=o.tenant_id
WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0 AND o.available_at_ns <= ?
AND o.lease_expires_at_ns <= ?
AND b.revision=?
ORDER BY o.available_at_ns,o.created_at_ns,o.id LIMIT ?`
	assertPlan(t, raw, claim, []any{pendingProbeNow, pendingProbeNow, 1, 32},
		"audit_governance_pending_claim_idx", "SCAN o", "SCAN audit_governance_outbox")
	// Lag path — OldestPendingAuditGovernance's MIN shape. On SQLite the real
	// joined query is served by the pre-existing due_idx (delivered_at_ns=0
	// scan; unchanged from baseline — the D3 probe harness's pending_lag_idx
	// plan for this query was measured after it dropped due_idx mid-section,
	// a schema state the real 0039 never has), so the joined probe pins the
	// no-full-table-scan property and the isolated form pins that
	// (created_at_ns) is the covering MIN path on this index.
	lag := `SELECT MIN(o.created_at_ns)
FROM audit_governance_outbox o JOIN audit_governance_bindings b ON b.tenant_id=o.tenant_id
WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`
	assertPlan(t, raw, lag, nil, "", "SCAN o", "SCAN audit_governance_outbox")
	lagIsolated := `SELECT MIN(o.created_at_ns)
FROM audit_governance_outbox o
WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`
	assertPlan(t, raw, lagIsolated, nil,
		"audit_governance_pending_lag_idx", "SCAN o", "SCAN audit_governance_outbox")
}

func TestAuditGovernanceFailedFactReadsBackOneAttempt(t *testing.T) {
	// REQ-5.3a: the "exactly 1 attempt" anchor at the repository level —
	// claim increments attempts once, FailAuditGovernance is the sole writer
	// of failed_at_ns, and both land on the same row.
	ctx := context.Background()
	store, raw := openPendingIdxStore(t)
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "probe-digest", []AuditGovernanceBindingState{
		{TenantID: "acme", State: AuditGovernanceBindingActive},
	}); err != nil {
		t.Fatal(err)
	}
	fact := AuditGovernanceFact{
		ID: uuid.NewString(), TenantID: "acme", OriginKind: AuditOriginAdmin,
		OriginID: 42, FactKind: "security", Action: "tenant.status",
		OccurredAt: time.Now().UTC(),
	}
	inserted, err := store.EnqueueAuditGovernance(ctx, fact)
	if err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim len=%d err=%v", len(claimed), err)
	}
	if err := store.FailAuditGovernance(ctx, claimed[0].ID, "worker", "token", "probe"); err != nil {
		t.Fatal(err)
	}
	var failedAtNs, attempts int64
	if err := raw.QueryRowContext(ctx,
		`SELECT failed_at_ns, attempts FROM audit_governance_outbox WHERE id = ?`,
		claimed[0].ID).Scan(&failedAtNs, &attempts); err != nil {
		t.Fatal(err)
	}
	if failedAtNs <= 0 {
		t.Fatalf("failed_at_ns=%d want > 0", failedAtNs)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want exactly 1", attempts)
	}
}

func TestAuditGovernance0043DeviationHeaderPinned(t *testing.T) {
	// REQ-5.4: the documented-deviation path is pinned, not silently re-shipped.
	for _, file := range []string{
		"migrations/sqlite/0043_audit_governance_pending_partial_index.up.sql",
		"migrations/postgres/0043_audit_governance_pending_partial_index.up.sql",
	} {
		body, err := migrationsFS.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, token := range []string{"failed_at_ns", "status", "dead_at", "implementation-gate"} {
			if !strings.Contains(string(body), token) {
				t.Errorf("%s header missing token %q", file, token)
			}
		}
	}
	for _, file := range []string{
		"migrations/sqlite/0042_audit_governance_terminal_failed.up.sql",
		"migrations/postgres/0042_audit_governance_terminal_failed.up.sql",
	} {
		body, err := migrationsFS.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(body), "failed_at_ns") {
			t.Errorf("%s no longer carries failed_at_ns", file)
		}
		if strings.Contains(string(body), "CREATE INDEX") {
			t.Errorf("%s re-shipped the partial index inside 0042", file)
		}
	}
}
