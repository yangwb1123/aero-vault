package s3compat

// B3-3/GAP-2: adapter governance e2e for the fail-closed DELETE flow
// (docs/requirements/s3compat-governance-delete-flow-e2e-v1.spec.md §4).
//
// Empirically corrected acceptance (spec §2): an allowed DELETE contributes
// exactly ONE audit_governance_outbox row (file.deleted) — the "2 facts"
// (deleted@1.1 + notify@1.1) live in event_outbox. PUT+DELETE totals 2 rows.
// Every delete-row read goes through governanceOutboxRowForAction (Finding A):
// after PUT+DELETE the same bucket/key matches both rows, so the unfiltered
// QueryRow helper returns an unspecified row.
//
// Detectors (design §3): FM-1 event-path breakage (200+0 rows), FM-2 double
// emit, FM-3 admin double-write, FM-4 occurred canonicalization, FM-5
// conflict-fold, FM-6 claim predicate, FM-7 denied-delete leakage, FM-8 gap
// ordering (Action predicates only — E8: the delete's audit_log row surfaces
// as an admin-kind gap, so len==1 assertions are banned), FM-9 sqlite lock
// (second connection, strictly sequential, no t.Parallel, per-test DBs).

import (
	"context"
	"database/sql"
	"encoding/xml"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestS3CompatAuditGovernanceDeleteFlow pins T-4.1 (capture + deterministic
// fact ID on the DELETE path) and T-4.2 (T-4 gap reuse for a deleted origin).
func TestS3CompatAuditGovernanceDeleteFlow(t *testing.T) {
	ctx := context.Background()
	srv, store, dsn := newGovernanceE2EServer(t, "active")

	if resp, _ := do(t, "PUT", srv.URL+"/b/k.txt", []byte("hello"), nil); resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	// Capture the objects-row ID BEFORE the hard delete removes the row
	// (HardDeleteObjectWithEvent DELETEs from objects, event_outbox.go:131).
	objID := governanceObjectID(t, dsn, "b", "k.txt")
	if resp, _ := do(t, "DELETE", srv.URL+"/b/k.txt", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d want 204 (handler.go DeleteObject)", resp.StatusCode)
	}

	// T-4.1 ② / FR-1: total == 2, both file-kind rows for the default tenant,
	// zero admin rows (FM-3), one row per action (FM-2 double emit).
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox`); n != 2 {
		t.Fatalf("gov rows=%d want 2 (PUT created + DELETE deleted; FM-1 event path)", n)
	}
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox WHERE origin_kind='file' AND tenant_id='default'`); n != 2 {
		t.Fatalf("file/tenant rows=%d want 2", n)
	}
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox WHERE origin_kind='admin'`); n != 0 {
		t.Fatalf("admin rows=%d want 0 (FM-3: delete audit_log must not double-write)", n)
	}
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox WHERE fact_kind!='file'`); n != 0 {
		t.Fatalf("non-file fact_kind rows=%d want 0", n)
	}
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox WHERE action='file.created'`); n != 1 {
		t.Fatalf("file.created rows=%d want 1 (FM-2)", n)
	}
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox WHERE action='file.deleted'`); n != 1 {
		t.Fatalf("file.deleted rows=%d want 1 (FM-2)", n)
	}

	// T-4.1 ③ / ④: the delete row (type-filtered read) — origin is the
	// object_events 'deleted' row, occurred is canonicalized (REQ-2 parity),
	// and the ID is the store-authoritative row recompute (REQ-3).
	found, id, originID, occurredNS, action, tenantID, createdRaw, _ :=
		governanceOutboxRowForAction(t, dsn, "b", "k.txt", "file.deleted")
	if !found || action != "file.deleted" || tenantID != "default" || originID <= 0 {
		t.Fatalf("delete row found=%v action=%q tenant=%q origin=%d", found, action, tenantID, originID)
	}
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM object_events WHERE id=? AND type='deleted'`, originID); n != 1 {
		t.Fatalf("origin event rows=%d want 1 of type deleted", n)
	}
	created, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		t.Fatalf("parse origin created_at %q: %v", createdRaw, err)
	}
	if occurredNS != created.UnixNano() {
		t.Fatalf("occurred_at_ns=%d != created_at .UnixNano()=%d (REQ-2 canonicalization, FM-4)", occurredNS, created.UnixNano())
	}
	expectedSource := e2eSourceID(string(testShareSecret), e2eGovernanceTenant)
	expectedID := repository.DeterministicFactID(expectedSource, "default",
		"file.deleted", "file", originID, created)
	if id != expectedID || !e2eFactIDPattern.MatchString(id) {
		t.Fatalf("delete id=%q want recompute=%q (store-authoritative determinism)", id, expectedID)
	}
	_, createdID, _, _, _, _, _, _ := governanceOutboxRowForAction(t, dsn, "b", "k.txt", "file.created")
	if createdID == id {
		t.Fatalf("created and deleted rows share id=%q", id)
	}

	// T-4.1 ⑤ carrier clarification (FR-1): the "2 facts" live in
	// event_outbox, originated on the objects row id — never the governance
	// origin_id (which is the object_events row id).
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM event_outbox`); n != 2 {
		t.Fatalf("event_outbox rows=%d want 2 (deleted@1.1 + notify@1.1)", n)
	}
	for _, evType := range []repository.OutboxEventType{repository.EventTypeFileDeleted11, repository.EventTypeFileNotify11} {
		if n := govCount(t, dsn, `SELECT COUNT(*) FROM event_outbox WHERE origin_id=? AND event_type=?`, objID, string(evType)); n != 1 {
			t.Fatalf("event_outbox %s rows=%d want 1 (origin=objects id %d)", evType, n, objID)
		}
	}
	if objID == originID {
		t.Fatalf("objects id %d collides with governance origin id", objID)
	}

	// T-4.2 ①: prune the delete row (the T-4 bypass — no delivered-origin
	// tombstone) so the gap path must resurface it.
	prune, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open prune db: %v", err)
	}
	defer prune.Close()
	if _, err := prune.Exec(`DELETE FROM audit_governance_outbox WHERE action='file.deleted'`); err != nil {
		t.Fatalf("prune delete row: %v", err)
	}

	// T-4.2 ②: gap selection by Action predicate (E8 trap: the delete's
	// audit_log row surfaces as an admin-kind gap, so len==1 is banned).
	gaps, err := store.ListAuditGovernanceGaps(ctx, e2eGovernanceTenant, 10)
	if err != nil {
		t.Fatalf("list gaps: %v", err)
	}
	var g *repository.AuditGovernanceGap
	for i := range gaps {
		if gaps[i].Action == "file.deleted" && gaps[i].OriginKind == "file" {
			g = &gaps[i]
			break
		}
	}
	if g == nil {
		t.Fatalf("no file.deleted gap in %+v", gaps)
	}
	if g.OriginID != originID || g.OccurredAt.UnixNano() != occurredNS {
		t.Fatalf("file gap=%+v want origin=%d occurred=%d", g, originID, occurredNS)
	}
	admin := false
	for _, gap := range gaps {
		if gap.Action == "file.delete" && gap.OriginKind == "admin" {
			admin = true
			break
		}
	}
	if !admin {
		t.Fatalf("no admin file.delete gap in %+v (E8: audit row gap coexists)", gaps)
	}

	// T-4.2 ③: factFromGap-equivalent rebuild → EnqueueAuditGovernance
	// recomputes the ID store-authoritatively (write.go:119-121) from the
	// same six inputs, so the re-read is byte-identical. The hash value itself
	// is timestamp-derived — the pin is the equality property, not a literal.
	rebuilt := repository.AuditGovernanceFact{
		SourceID: expectedSource, TenantID: "default", OriginKind: "file", FactKind: "file",
		Action: g.Action, OriginID: g.OriginID, OccurredAt: g.OccurredAt,
	}
	inserted, err := store.EnqueueAuditGovernance(ctx, rebuilt)
	if err != nil || !inserted {
		t.Fatalf("re-enqueue delete fact: inserted=%v err=%v", inserted, err)
	}
	found, reID, _, _, _, _, _, recount := governanceOutboxRowForAction(t, dsn, "b", "k.txt", "file.deleted")
	if !found || recount != 1 || reID != id {
		t.Fatalf("re-enqueued found=%v count=%d id=%q want count=1 id=%q (byte-identical AC-2, FM-5)", found, recount, reID, id)
	}

	// T-4.2 ④: the dedupe branch folds a second enqueue to (false, nil) via
	// ON CONFLICT (origin_kind, origin_id) DO NOTHING (write.go:160).
	if again, err := store.EnqueueAuditGovernance(ctx, rebuilt); err != nil || again {
		t.Fatalf("duplicate enqueue inserted=%v err=%v want (false,nil) (FM-5)", again, err)
	}
}

// TestS3CompatAuditGovernanceDeleteClaimLag pins T-3: the delete row obeys the
// same claim/lag lifecycle as the PUT row — FailAuditGovernance lands the
// terminal state (failed_at_ns set), so a later claim never resurfaces it and
// OldestPendingAuditGovernance excludes it (E6 predicates, claim.go:78-80 /
// 216-218).
func TestS3CompatAuditGovernanceDeleteClaimLag(t *testing.T) {
	ctx := context.Background()
	srv, store, _ := newGovernanceE2EServer(t, "active")

	if resp, _ := do(t, "PUT", srv.URL+"/b/k.txt", []byte("hello"), nil); resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	if resp, _ := do(t, "DELETE", srv.URL+"/b/k.txt", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d want 204", resp.StatusCode)
	}

	// T-3 ① positive control (Finding C): the initial claim must return
	// exactly 2 rows at the fixture binding's revision (Revision: 1) — the
	// claim-0 negative below is only non-vacuous when this holds.
	claimed, err := store.ClaimAuditGovernance(ctx, "e2e-owner", "e2e-token", 1, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claim rows=%d want exactly 2 (created + deleted) — positive control", len(claimed))
	}
	var created, deleted *repository.AuditGovernanceFact
	for i := range claimed {
		switch claimed[i].Action {
		case "file.created":
			created = &claimed[i]
		case "file.deleted":
			deleted = &claimed[i]
		}
	}
	if created == nil || deleted == nil {
		t.Fatalf("claim must return both actions, got %+v", claimed)
	}

	// T-3 ②: terminal states — complete the created row, fail the deleted row.
	if err := store.CompleteAuditGovernance(ctx, created.ID, "e2e-owner", "e2e-token"); err != nil {
		t.Fatalf("complete created: %v", err)
	}
	if err := store.FailAuditGovernance(ctx, deleted.ID, "e2e-owner", "e2e-token", "probe-fail"); err != nil {
		t.Fatalf("fail deleted: %v", err)
	}

	// T-3 ③: a new owner claims nothing (the failed row is excluded by
	// failed_at_ns=0, the delivered row is gone) and lag excludes it too.
	again, err := store.ClaimAuditGovernance(ctx, "e2e-owner2", "e2e-token2", 1, 10, time.Minute)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-claim rows=%d want 0 (dead delete row must not resurface, FM-6)", len(again))
	}
	if _, pending, err := store.OldestPendingAuditGovernance(ctx); err != nil || pending {
		t.Fatalf("oldest pending=%v want (_, false, nil) — dead row excluded from lag", pending)
	}
}

// TestS3CompatGovernanceDeniedDeleteZeroRows pins the Gate acceptance (spec
// §4 Gate): the adapter gate rejects the DELETE with 403 BEFORE any service
// call (authorizeDelete, authz.go:27-45, invoked at policy.go:70-71), so every
// durable surface stays untouched: no governance rows, no
// object_events/audit_log/event_outbox rows, object alive.
//
// Fixture attribution (design §2.7): the service-side authorizer stays
// allowAllProvider{} while the router third arg is denyAllProvider{} — the 403
// MUST come from the adapter gate. Flipping either side self-neutralizes the
// detector: service-side deny still yields 403+zero rows (from the service
// gate) while silently skipping the adapter gate; router-side allow leaks the
// delete. Both-denied and both-allowed both blind the detector.
func TestS3CompatGovernanceDeniedDeleteZeroRows(t *testing.T) {
	srv, _, dsn := newGovernanceE2EServerWithAuthz(t, "active", denyAllProvider{})

	if resp, _ := do(t, "PUT", srv.URL+"/b/k.txt", []byte("hello"), nil); resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	resp, body := do(t, "DELETE", srv.URL+"/b/k.txt", nil, nil)
	assertAccessDenied(t, resp, body)

	// Gate ③: gov total stays 1 (the PUT's file.created) with NO file.deleted
	// row. The message distinguishes the two failure meanings (Finding D):
	// 0 rows = 'capture active' (FM-1 wiring broken); >1 rows or a
	// file.deleted row = 'adapter gate' (FM-7 leak).
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox`); n != 1 {
		t.Fatalf("gov rows=%d want 1 (capture active: PUT captured 1; adapter gate: denied DELETE added 0)", n)
	}
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox WHERE action='file.deleted'`); n != 0 {
		t.Fatalf("denied delete wrote %d file.deleted gov rows (FM-7 adapter-gate leak)", n)
	}

	// Gate ④: the shared zero-side-effect detector (spec Gate.4 — the only
	// non-vacuous execution point of its +1 governance assertion) plus
	// event_outbox zero counts for both fact types, plus the object survives.
	obj := repository.Object{TenantID: "default", Bucket: "b", Key: "k.txt"}
	assertZeroSideEffects(t, dsn, obj)
	objID := governanceObjectID(t, dsn, "b", "k.txt")
	for _, evType := range []repository.OutboxEventType{repository.EventTypeFileDeleted11, repository.EventTypeFileNotify11} {
		if n := govCount(t, dsn, `SELECT COUNT(*) FROM event_outbox WHERE origin_id=? AND event_type=?`, objID, string(evType)); n != 0 {
			t.Fatalf("denied delete wrote %d %s rows", n, evType)
		}
	}
	if resp, _ := do(t, "GET", srv.URL+"/b/k.txt", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("object must survive denied delete, got %d", resp.StatusCode)
	}

	// Gate ⑤: batch ?delete with both keys denied → 200 shell + per-key
	// AccessDenied, zero rows anywhere, both objects survive.
	do(t, "PUT", srv.URL+"/b/k2.txt", []byte("y"), nil)
	obj2 := repository.Object{TenantID: "default", Bucket: "b", Key: "k2.txt"}
	req, _ := xml.Marshal(deleteRequest{Objects: []deleteRequestObject{{Key: "k.txt"}, {Key: "k2.txt"}}})
	resp, body = do(t, "POST", srv.URL+"/b/?delete", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d want 200 shell body=%s", resp.StatusCode, body)
	}
	var dr deleteResult
	if err := xml.Unmarshal(body, &dr); err != nil {
		t.Fatalf("parse delete result: %v body=%s", err, body)
	}
	if len(dr.Deleted) != 0 || len(dr.Errors) != 2 {
		t.Fatalf("batch result: deleted=%d errors=%d body=%s", len(dr.Deleted), len(dr.Errors), body)
	}
	for _, o := range []repository.Object{obj, obj2} {
		assertZeroSideEffects(t, dsn, o)
	}
	if n := govCount(t, dsn, `SELECT COUNT(*) FROM audit_governance_outbox WHERE action='file.deleted'`); n != 0 {
		t.Fatalf("denied batch wrote %d file.deleted gov rows", n)
	}
	if resp, _ := do(t, "GET", srv.URL+"/b/k2.txt", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("k2.txt must survive, got %d", resp.StatusCode)
	}
}

// govCount runs a scalar COUNT query on a second connection to the same
// sqlite file (the established second-connection pattern; ? placeholders only
// — I1: this SQL bypasses s.rebind, repository/sql.go:42).
func govCount(t *testing.T, dsn, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open gov db: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// governanceObjectID reads the objects-table row id for the given object via
// a second connection. It must be called BEFORE a hard delete (the unversioned
// DELETE removes the objects row, event_outbox.go:131).
func governanceObjectID(t *testing.T, dsn, bucket, key string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open objects db: %v", err)
	}
	defer db.Close()
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM objects WHERE tenant_id='default' AND bucket=? AND key=?`, bucket, key).Scan(&id); err != nil {
		t.Fatalf("read objects id: %v", err)
	}
	return id
}
