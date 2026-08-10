//go:build integration

package integration

// PG behavioral pin (design: docs/auto/designs/internal-integration-pg-dead-row-t3-pin.md):
// a failed (dead) outbox row must be excluded from the claim WHERE
// (failed_at_ns=0), from OldestPendingAuditGovernance, and from the
// runtime's Ready/backlog-gauge probes — on the real Postgres dialect, not
// only on SQLite. Selection: the TestPostgres prefix matches the gate regex
// (.github/workflows/integration-pg.yml: -run 'TestPostgres|TestPg').
//
// Sibling file: audit_governance_postgres_test.go is at 483/500 lines, so
// these tests live here. Isolation: freshRepo (schema reset + migrations)
// per test, per-test UUID tenant, -count=1 in the gate. The claim path is
// global across bound tenants (bindings join + revision only), so exactly
// one fact is seeded per phase.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// seedClaimFail inserts one governance fact under an ACTIVE binding, claims it
// (pre-fail discriminator: the row is otherwise claimable — proves the
// available/lease predicates admit it), and terminalizes it via
// FailAuditGovernance. Precondition: binding at revision 1 already applied
// (explicitly in T-1/T-2; by auditgovernance.New in T-3). Claim revision must
// be 1 — the revision the binding was applied with (ClaimAuditGovernance's
// 4th parameter matches b.revision in the claim SQL, not a row id).
// Returns the origin event id and the 32-hex outbox id (from the claim).
func seedClaimFail(t *testing.T, store repository.AuditGovernanceStore, tenant string) (int64, string) {
	t.Helper()
	ctx := context.Background()
	event := repository.Event{TenantID: tenant, Bucket: "default",
		Key: "pg-dead-" + uuid.NewString(), Type: repository.EventCreated,
		Payload: map[string]string{"size": "7", "backend": "local"}}
	fact := repository.AuditGovernanceFact{TenantID: tenant, FactKind: "file",
		Action: "file.created", TargetDigest: "hmac-sha256:target",
		OccurredAt: time.Now().UTC()} // ID overwritten store-authoritatively.
	originID, err := store.InsertEventWithGovernance(ctx, event, fact)
	if err != nil || originID <= 0 {
		t.Fatalf("insert: id=%d err=%v", originID, err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("pre-fail claim=%d err=%v want=1", len(claimed), err)
	}
	if err := store.FailAuditGovernance(ctx, claimed[0].ID, "worker", "token", "conflict:true"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	return originID, claimed[0].ID
}

// REQ-1: the PG claim excludes dead rows. The pre-fail claim of exactly 1 row
// is the discriminator (the row is otherwise claimable); the raw-SQL state
// proof makes the dead state the provable cause (write-side pin, not an
// assumption about FailAuditGovernance); the post-fail claim with a rotated
// identity must return 0 rows.
func TestPostgresAuditGovernanceDeadRowExcludedFromClaim(t *testing.T) {
	ctx := context.Background()
	repo, raw := freshRepo(t)
	store := repo.(repository.AuditGovernanceStore)
	tenant := "audit-pg-" + uuid.NewString()
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-1", []repository.AuditGovernanceBindingState{{
		TenantID: tenant, State: repository.AuditGovernanceBindingActive,
	}}); err != nil {
		t.Fatalf("apply bindings: %v", err)
	}
	_, outboxID := seedClaimFail(t, store, tenant)

	// Raw state proof: Fail persisted failed_at_ns>0 and cleared the lease.
	var failedAtNs, leaseExpiresAtNs int64
	if err := raw.QueryRowContext(ctx,
		`SELECT failed_at_ns, lease_expires_at_ns FROM audit_governance_outbox WHERE id=$1`,
		outboxID).Scan(&failedAtNs, &leaseExpiresAtNs); err != nil {
		t.Fatalf("raw state read: %v", err)
	}
	if failedAtNs <= 0 {
		t.Fatalf("failed_at_ns=%d want >0 (Fail did not persist the dead state)", failedAtNs)
	}
	if leaseExpiresAtNs != 0 {
		t.Fatalf("lease_expires_at_ns=%d want=0 (Fail did not clear the lease)", leaseExpiresAtNs)
	}

	// Post-fail claim with a fresh identity: the dead row must be excluded.
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token-2", 1, 10, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("post-fail claim=%d err=%v want=0 (dead row re-claimed; WHERE lost failed_at_ns=0)", len(claimed), err)
	}

	// Negative control (F8 on PG): a stale identity must not terminalize a
	// dead row — the dead row fails every fail-WHERE predicate
	// (failed_at_ns=0, live lease, matching owner/token), so Fail must error.
	if err := store.FailAuditGovernance(ctx, outboxID, "stale-owner", "stale-token", "x"); err == nil {
		t.Fatal("stale-identity fail on a dead row succeeded — fencing lost")
	}
}

// REQ-2: OldestPendingAuditGovernance excludes dead rows. The ok=true result
// BEFORE the fail is the load-bearing discriminator (outbox non-empty, row
// pending-eligible); after the fail the same single row must be absent from
// MIN(created_at_ns).
func TestPostgresAuditGovernanceDeadRowExcludedFromLag(t *testing.T) {
	ctx := context.Background()
	repo, _ := freshRepo(t)
	store := repo.(repository.AuditGovernanceStore)
	tenant := "audit-pg-" + uuid.NewString()
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-1", []repository.AuditGovernanceBindingState{{
		TenantID: tenant, State: repository.AuditGovernanceBindingActive,
	}}); err != nil {
		t.Fatalf("apply bindings: %v", err)
	}
	seedEvent := repository.Event{TenantID: tenant, Bucket: "default",
		Key: "pg-lag-" + uuid.NewString(), Type: repository.EventCreated,
		Payload: map[string]string{"size": "7", "backend": "local"}}
	fact := repository.AuditGovernanceFact{TenantID: tenant, FactKind: "file",
		Action: "file.created", TargetDigest: "hmac-sha256:target",
		OccurredAt: time.Now().UTC()}
	if _, err := store.InsertEventWithGovernance(ctx, seedEvent, fact); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Discriminator first (ordering is load-bearing): the row is pending.
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || !ok {
		t.Fatalf("pre-fail OldestPending ok=%v err=%v want ok=true", ok, err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "worker", "token", 1, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%d err=%v want=1", len(claimed), err)
	}
	if err := store.FailAuditGovernance(ctx, claimed[0].ID, "worker", "token", "conflict:true"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("post-fail OldestPending ok=%v err=%v want ok=false (dead row feeds MIN(created_at_ns))", ok, err)
	}
}

// REQ-3: a dead-only backlog must not degrade the runtime's readiness or
// gauge. Config uses the C1-corrected literal: ClientSecretEnv is mandatory
// (envNamePattern + AUDIT_GOVERNANCE_CLIENT_SECRET_ prefix), ClientSecret
// non-empty and distinct from HMACKey (42B ∈ [32,4096]).
func TestPostgresAuditGovernanceDeadOnlyBacklogRuntimeReady(t *testing.T) {
	ctx := context.Background()
	repo, _ := freshRepo(t)
	store := repo.(repository.AuditGovernanceStore)
	tenant := "audit-pg-" + uuid.NewString()

	cfg := config.AuditGovernanceConfig{
		Enabled: true,
		BaseURL: "http://127.0.0.1:1", TokenURL: "http://127.0.0.1:1/token",
		HMACKey:  "audit-governance-hmac-key-32-bytes-minimum", // 42B ≥ 32, ≠ any secret
		Revision: 1,
		Bindings: []config.AuditGovernanceBinding{{
			TenantID: tenant, ClientID: "vault-audit",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_PG", // C1: required pattern
			ClientSecret:    "machine-secret", State: "active",
		}},
		HTTPTimeoutSeconds: 1, PollMilliseconds: 10, BatchSize: 10,
		ClaimTTLSeconds: 3, InitialBackoffSeconds: 1, MaxBackoffSeconds: 2,
		MaxLagSeconds: 4, ReconcileBatchSize: 20,
		DeliveredRetentionSeconds: 3600, CleanupIntervalSeconds: 60, CleanupBatchSize: 20,
	}
	rt, err := auditgovernance.New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()                // never Start()ed — New performs zero network I/O (F3)
	seedClaimFail(t, store, tenant) // backlog is now dead-only

	// Step 5: the direct store probe reports no pending backlog.
	if age, ok, err := rt.PendingBacklogAge(ctx); err != nil || ok || age != 0 {
		t.Fatalf("PendingBacklogAge age=%v ok=%v err=%v want (0,false,nil)", age, ok, err)
	}
	// Steps 6–7: Ready() must not degrade on a dead-only backlog. BacklogAge()
	// is a zero-I/O cache read whose initial value is 0, so the assertion is
	// bound by the two-state probe below, which observes a non-zero record
	// then a zero record — not just the never-probed zero.
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready with dead-only backlog: %v", err)
	}
	if rt.Degraded() {
		t.Fatal("dead-only backlog degraded readiness")
	}
	if age := rt.BacklogAge(); age != 0 {
		t.Fatalf("BacklogAge=%v want=0 after dead-only probe", age)
	}

	// Two-state cache probe: a live pending row must record a non-zero age
	// (still not degraded — age < maxLag), and failing it must flip the cache
	// back to (false, 0). This binds the zero assertion to an actual
	// recordDegraded write, closing the initial-zero-value coincidence.
	liveEvent := repository.Event{TenantID: tenant, Bucket: "default",
		Key: "pg-live-" + uuid.NewString(), Type: repository.EventCreated,
		Payload: map[string]string{"size": "3", "backend": "local"}}
	liveFact := repository.AuditGovernanceFact{TenantID: tenant, FactKind: "file",
		Action: "file.created", TargetDigest: "hmac-sha256:live",
		OccurredAt: time.Now().UTC()}
	if _, err := store.InsertEventWithGovernance(ctx, liveEvent, liveFact); err != nil {
		t.Fatalf("insert live fact: %v", err)
	}
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready with live backlog: %v", err)
	}
	if age := rt.BacklogAge(); age <= 0 {
		t.Fatalf("live backlog age=%v want >0", age)
	}
	if rt.Degraded() {
		t.Fatal("live backlog within maxLag must not degrade")
	}
	liveClaimed, err := store.ClaimAuditGovernance(ctx, "probe", "probe-tok", 1, 1, time.Minute)
	if err != nil || len(liveClaimed) != 1 {
		t.Fatalf("live claim=%d err=%v want=1", len(liveClaimed), err)
	}
	if err := store.FailAuditGovernance(ctx, liveClaimed[0].ID, "probe", "probe-tok", "terminal:true"); err != nil {
		t.Fatalf("live fail: %v", err)
	}
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready after live row terminalized: %v", err)
	}
	if age := rt.BacklogAge(); age != 0 {
		t.Fatalf("post-live-fail BacklogAge=%v want=0", age)
	}
	if rt.Degraded() {
		t.Fatal("dead-only backlog must not degrade")
	}
}
