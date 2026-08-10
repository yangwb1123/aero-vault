package auditgovernance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// factIDPattern is the campaign's ID shape: 32 lowercase hex chars.
var factIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

const factIDHMACKey = "deterministic-fact-id-test-key-32bytes-min"

// factIDStore opens a fresh SQLite store with an active acme binding,
// mirroring the runtime_test.go harness (repository.Open + Migrate + binding).
func factIDStore(t *testing.T) repository.AuditGovernanceStore {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "factid.db"))
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
	return store
}

// claimFailPrune runs the T-4 bypass cycle: claim → terminal fail → retention
// prune (no tombstone) → the gap resurfaces. Returns the claimed fact.
func claimFailPrune(
	t *testing.T, store repository.AuditGovernanceStore, claimed []repository.AuditGovernanceFact,
) {
	t.Helper()
	ctx := context.Background()
	if len(claimed) != 1 {
		t.Fatalf("claimed facts=%d want=1", len(claimed))
	}
	if err := store.FailAuditGovernance(ctx, claimed[0].ID, "owner", "token", "boom"); err != nil {
		t.Fatalf("fail claim: %v", err)
	}
	if n, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(time.Hour), 10); err != nil || n != 1 {
		t.Fatalf("prune failed row: n=%d err=%v want n=1", n, err)
	}
}

// assertGapEqualsAtomic pins AC-1: the gap-rebuilt fact's deterministic ID
// equals the atomically captured fact's ID, both 32-hex.
func assertGapEqualsAtomic(
	t *testing.T, store repository.AuditGovernanceStore, redactor *redactor, claimed []repository.AuditGovernanceFact,
) {
	t.Helper()
	ctx := context.Background()
	gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("gaps=%+v err=%v want=1", gaps, err)
	}
	rebuilt := redactor.factFromGap(gaps[0], time.Now())
	if rebuilt.ID != claimed[0].ID {
		t.Fatalf("gap ID %q != atomic ID %q (origin=%d %s)", rebuilt.ID, claimed[0].ID, gaps[0].OriginID, gaps[0].OriginKind)
	}
	if !factIDPattern.MatchString(rebuilt.ID) {
		t.Fatalf("gap ID %q does not match %s", rebuilt.ID, factIDPattern)
	}
}

// TestDeterministicFactID_GapEqualsAtomic_Admin pins AC-1 for the admin
// origin: explicit CreatedAt is stored verbatim (audit_log.created_at TEXT),
// so REQ-2 canonicalization is lossless on both paths.
func TestDeterministicFactID_GapEqualsAtomic_Admin(t *testing.T) {
	ctx := context.Background()
	store := factIDStore(t)
	redactor, err := newRedactor(factIDHMACKey)
	if err != nil {
		t.Fatalf("new redactor: %v", err)
	}
	entry := repository.AuditEntry{TenantID: "acme", Actor: "operator", Action: "key.add",
		Target: "acme", CreatedAt: "2026-08-08T01:17:41.123456789Z"}
	fact := redactor.factFromAudit(entry, time.Now())
	if fact.ID != "" {
		t.Fatalf("constructor minted an ID: %q (store is the ID authority)", fact.ID)
	}
	if fact.SourceID == "" || !strings.HasPrefix(fact.SourceID, SourcePrefix+".") {
		t.Fatalf("SourceID %q not a source-system ID", fact.SourceID)
	}
	if err := store.RecordAuditWithGovernance(ctx, entry, fact); err != nil {
		t.Fatalf("record atomic audit: %v", err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "owner", "token", 1, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !factIDPattern.MatchString(claimed[0].ID) {
		t.Fatalf("atomic ID %q does not match %s", claimed[0].ID, factIDPattern)
	}
	claimFailPrune(t, store, claimed)
	assertGapEqualsAtomic(t, store, redactor, claimed)
}

// TestDeterministicFactID_GapEqualsAtomic_File pins AC-1 for the file origin
// with a zero CreatedAt event — the production shape (service emit never sets
// it). REQ-2 canonicalization makes the atomic path's occurred byte-identical
// to the DB default the gap path parses; without it the IDs diverge whenever
// the constructor's ns now and the DB's ms default land in different seconds.
func TestDeterministicFactID_GapEqualsAtomic_File(t *testing.T) {
	ctx := context.Background()
	store := factIDStore(t)
	redactor, err := newRedactor(factIDHMACKey)
	if err != nil {
		t.Fatalf("new redactor: %v", err)
	}
	event := repository.Event{TenantID: "acme", Bucket: "default", Key: "k.txt",
		Type: repository.EventCreated, Payload: map[string]string{"size": "12", "backend": "local"}}
	fact := redactor.factFromEvent(event, time.Now())
	if fact.ID != "" {
		t.Fatalf("constructor minted an ID: %q (store is the ID authority)", fact.ID)
	}
	if _, err := store.InsertEventWithGovernance(ctx, event, fact); err != nil {
		t.Fatalf("record atomic event: %v", err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "owner", "token", 1, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !factIDPattern.MatchString(claimed[0].ID) {
		t.Fatalf("atomic ID %q does not match %s", claimed[0].ID, factIDPattern)
	}
	claimFailPrune(t, store, claimed)
	assertGapEqualsAtomic(t, store, redactor, claimed)
}

// TestDeterministicFactID_PruneReenqueueSameID pins AC-3(b): after the
// retention prune re-creates the outbox row via the gap path, the re-created
// row's ID equals the pre-prune ID — the sink folds the re-POST to Duplicate
// (T-4) instead of double-ledgering.
func TestDeterministicFactID_PruneReenqueueSameID(t *testing.T) {
	ctx := context.Background()
	store := factIDStore(t)
	redactor, err := newRedactor(factIDHMACKey)
	if err != nil {
		t.Fatalf("new redactor: %v", err)
	}
	entry := repository.AuditEntry{TenantID: "acme", Action: "tenant.status",
		CreatedAt: "2026-08-08T01:17:41.123456789Z"}
	fact := redactor.factFromAudit(entry, time.Now())
	if err := store.RecordAuditWithGovernance(ctx, entry, fact); err != nil {
		t.Fatalf("record atomic audit: %v", err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "owner", "token", 1, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	prePruneID := claimed[0].ID
	claimFailPrune(t, store, claimed)
	gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("gap did not resurface: gaps=%+v err=%v", gaps, err)
	}
	rebuilt := redactor.factFromGap(gaps[0], time.Now())
	if rebuilt.ID != prePruneID {
		t.Fatalf("rebuilt ID %q != pre-prune ID %q", rebuilt.ID, prePruneID)
	}
	inserted, err := store.EnqueueAuditGovernance(ctx, rebuilt)
	if err != nil || !inserted {
		t.Fatalf("re-enqueue: inserted=%v err=%v", inserted, err)
	}
	again, err := store.ClaimAuditGovernance(ctx, "owner2", "token2", 1, 10, time.Minute)
	if err != nil || len(again) != 1 {
		t.Fatalf("re-created row claim=%+v err=%v", again, err)
	}
	if again[0].ID != prePruneID {
		t.Fatalf("re-created row ID %q != pre-prune ID %q", again[0].ID, prePruneID)
	}
}

// TestNoUUIDInFactsGo pins AC-2: facts.go must contain no uuid reference —
// the three constructors stopped minting IDs (the only uuid.NewString left in
// the package is the per-claim token at relay.go:62, out of scope).
func TestNoUUIDInFactsGo(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "facts.go"))
	if err != nil {
		t.Fatalf("read facts.go: %v", err)
	}
	if strings.Contains(string(src), "uuid") {
		t.Fatal("facts.go must not reference uuid (deterministic fact IDs, AC-2)")
	}
}

// ── T-4 pin: quarantine-shaped admin gap (born on the gap path) ──────────────

// quarantineDeleteFacts mirrors the repository-package validDeleteFacts
// (event_outbox_test.go:54, package-private, not importable here):
// validateOutboxFacts only checks event type, origin id, tenant, payload size
// and schema_version=="1.1", and insertOutboxFacts stores the payload
// byte-exact — so this minimal local builder satisfies the delete transaction.
func quarantineDeleteFacts(obj repository.Object, tenant string) []repository.OutboxFact {
	deleted := fmt.Sprintf(`{"schema_version":"1.1","event_type":"vault.file.deleted@1.1","tenant":%q,"bucket":%q,"key":%q,"object_id":%d}`,
		tenant, obj.Bucket, obj.Key, obj.ID)
	notify := fmt.Sprintf(`{"schema_version":"1.1","event_type":"vault.file.notify@1.1","tenant":%q,"bucket":%q,"key":%q}`,
		tenant, obj.Bucket, obj.Key)
	return []repository.OutboxFact{
		{EventType: repository.EventTypeFileDeleted11, OriginID: obj.ID, TenantID: tenant, Payload: []byte(deleted)},
		{EventType: repository.EventTypeFileNotify11, OriginID: obj.ID, TenantID: tenant, Payload: []byte(notify)},
	}
}

// seedQuarantineGap reproduces the production quarantine write (object_worker.go
// quarantineAuditEntry) through the real producer surface: UpsertObject then
// SoftDeleteObjectByIDWithEvent with an empty CreatedAt, so insertAuditEntry
// stamps RFC3339Nano (audit.go:23) — the only admin row born on the gap path.
// The seed surface lives on Repository only, hence the type assertion
// (dynamic type *sqlStore implements both interfaces).
func seedQuarantineGap(t *testing.T, store repository.AuditGovernanceStore) (repository.Object, repository.AuditEntry) {
	t.Helper()
	ctx := context.Background()
	repo := store.(repository.Repository)
	obj, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: "acme", Bucket: "b", Key: "k", VersionID: "v-1",
		Backend: "local", StorageKey: "acme/b/k@v-1", Size: 42,
		ETag: "etag-1", ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("seed object: %v", err)
	}
	entry := repository.AuditEntry{
		TenantID: "acme", Actor: "system:antivirus",
		Action: repository.AuditActionFileDelete, Target: "b/k", Detail: "av_infected",
	}
	if err := repo.SoftDeleteObjectByIDWithEvent(ctx, obj.ID, entry, quarantineDeleteFacts(obj, "acme")); err != nil {
		t.Fatalf("quarantine seed: %v", err)
	}
	return obj, entry
}

// TestDeterministicFactID_QuarantineGapScanStable pins REQ-1/REQ-2/REQ-3a:
// the quarantine-shaped admin gap must yield a scan-stable deterministic fact
// ID. Two scans with nowA/nowB in different second buckets (nowB = nowA + 5min
// — the formula truncates to the second) must produce byte-identical IDs equal
// to the formula recompute from DB fields, all before any enqueue; enqueue
// then dedupes (ON CONFLICT (origin_kind,origin_id) DO NOTHING) to exactly one
// outbox row carrying the stable ID.
func TestDeterministicFactID_QuarantineGapScanStable(t *testing.T) {
	ctx := context.Background()
	store := factIDStore(t)
	redactor, err := newRedactor(factIDHMACKey)
	if err != nil {
		t.Fatalf("new redactor: %v", err)
	}
	repo := store.(repository.Repository)
	_, entry := seedQuarantineGap(t, store)

	// Locate the seeded audit row via the public ListAudit surface and parse
	// created_at test-side — the independent cross-check (REQ-3a) proving the
	// store's gap-scan parse took the success path (not self-referential).
	rows, err := repo.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var auditRow *repository.AuditEntry
	for i := range rows {
		if rows[i].TenantID == entry.TenantID && rows[i].Actor == entry.Actor &&
			rows[i].Action == entry.Action && rows[i].Target == entry.Target &&
			rows[i].Detail == entry.Detail {
			auditRow = &rows[i]
			break
		}
	}
	if auditRow == nil {
		t.Fatalf("seeded audit row not found in %+v", rows)
	}
	parsed, err := time.Parse(time.RFC3339Nano, auditRow.CreatedAt)
	if err != nil {
		t.Fatalf("seed created_at %q not RFC3339Nano (stamp drift?): %v", auditRow.CreatedAt, err)
	}

	nowA := time.Now()
	nowB := nowA.Add(5 * time.Minute) // hard constraint: different second bucket

	// REQ-1: scan stability before any enqueue.
	gapsA, err := store.ListAuditGovernanceGaps(ctx, "acme", 10)
	if err != nil || len(gapsA) != 1 {
		t.Fatalf("gapsA=%+v err=%v want exactly 1 (quarantine admin gap)", gapsA, err)
	}
	if gapsA[0].OriginKind != repository.AuditOriginAdmin {
		t.Fatalf("gap origin kind %q want %q", gapsA[0].OriginKind, repository.AuditOriginAdmin)
	}
	if gapsA[0].OriginID != auditRow.ID {
		t.Fatalf("gap origin %d != audit row id %d", gapsA[0].OriginID, auditRow.ID)
	}
	if !gapsA[0].OccurredAt.Equal(parsed) {
		t.Fatalf("gap OccurredAt %v != test-side parse %v", gapsA[0].OccurredAt, parsed)
	}
	factA := redactor.factFromGap(gapsA[0], nowA)
	gapsB, err := store.ListAuditGovernanceGaps(ctx, "acme", 10) // still a gap: nothing enqueued yet
	if err != nil || len(gapsB) != 1 {
		t.Fatalf("gapsB=%+v err=%v want exactly 1", gapsB, err)
	}
	factB := redactor.factFromGap(gapsB[0], nowB)
	if factA.ID != factB.ID {
		t.Fatalf("gap ID %q (nowA) != %q (nowB): scan must be stable", factA.ID, factB.ID)
	}
	if !factIDPattern.MatchString(factA.ID) {
		t.Fatalf("gap ID %q does not match %s", factA.ID, factIDPattern)
	}
	// REQ-3a: parse-success path — the now() fallback was never consulted.
	if !factA.OccurredAt.Equal(parsed) || factA.OccurredAt.Equal(nowA.UTC()) {
		t.Fatalf("now() fallback consulted: OccurredAt=%v parsed=%v nowA=%v", factA.OccurredAt, parsed, nowA.UTC())
	}
	want := repository.DeterministicFactID(factA.SourceID, "acme",
		repository.AuditActionFileDelete, repository.AuditOriginAdmin, gapsA[0].OriginID, parsed)
	if factA.ID != want {
		t.Fatalf("gap ID %q != formula recompute %q", factA.ID, want)
	}

	// REQ-2: enqueue dedupes to exactly one outbox row with the stable ID.
	inserted1, err := store.EnqueueAuditGovernance(ctx, factA)
	if err != nil || !inserted1 {
		t.Fatalf("first enqueue: inserted=%v err=%v want true", inserted1, err)
	}
	inserted2, err := store.EnqueueAuditGovernance(ctx, factB)
	if err != nil || inserted2 {
		t.Fatalf("second enqueue: inserted=%v err=%v want false (ON CONFLICT)", inserted2, err)
	}
	claimed, err := store.ClaimAuditGovernance(ctx, "owner", "token", 1, 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != factA.ID {
		t.Fatalf("claimed=%+v err=%v want exactly 1 row with ID %q", claimed, err, factA.ID)
	}
}

// TestDeterministicFactID_QuarantineGapParseFallbackLoud pins REQ-3b: a
// deliberately drifted (space-separated, SQLite-default) created_at must make
// the silent fallback observably clock-dependent — the test fails loudly
// instead of minting a second ID. Runs on its own fresh store so the
// negative-control row cannot pollute Test 1's "exactly 1 gap" assertion.
// Note: "2026-08-08T01:17:41Z" parses under RFC3339Nano and is NOT a control.
func TestDeterministicFactID_QuarantineGapParseFallbackLoud(t *testing.T) {
	ctx := context.Background()
	store := factIDStore(t)
	redactor, err := newRedactor(factIDHMACKey)
	if err != nil {
		t.Fatalf("new redactor: %v", err)
	}
	repo := store.(repository.Repository)
	const drifted = "2026-08-08 01:17:41.123456789+00:00" // space-separated: RFC3339Nano parse MUST fail
	if err := repo.RecordAudit(ctx, repository.AuditEntry{
		TenantID: "acme", Actor: "system:antivirus", Action: repository.AuditActionFileDelete,
		Target: "b/k", Detail: "av_infected", CreatedAt: drifted,
	}); err != nil {
		t.Fatalf("record drifted audit: %v", err)
	}

	rows, err := repo.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	originID := int64(0)
	for _, row := range rows {
		if row.CreatedAt == drifted { // non-empty CreatedAt is stored verbatim (RecordAudit)
			originID = row.ID
			break
		}
	}
	if originID == 0 {
		t.Fatalf("drifted row not found in %+v", rows)
	}

	gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10)
	if err != nil {
		t.Fatalf("gaps: %v", err)
	}
	var gap *repository.AuditGovernanceGap
	for i := range gaps {
		if gaps[i].OriginID == originID {
			gap = &gaps[i]
			break
		}
	}
	if gap == nil {
		t.Fatalf("drifted gap not found in %+v", gaps)
	}
	if !gap.OccurredAt.IsZero() {
		t.Fatalf("store-level parse failure must be observable as zero OccurredAt (write.go:258 swallow)")
	}

	nowA := time.Now()
	nowB := nowA.Add(5 * time.Minute)
	factBadA := redactor.factFromGap(*gap, nowA)
	factBadB := redactor.factFromGap(*gap, nowB)
	if factBadA.ID == factBadB.ID {
		t.Fatalf("unparseable created_at %q made the fact ID clock-dependent; format drift must fail here, not silently mint a second ID", drifted)
	}
	if !factBadA.OccurredAt.Equal(nowA.UTC()) || !factBadB.OccurredAt.Equal(nowB.UTC()) {
		t.Fatalf("fallback did not stamp the scan clock: factA=%v nowA=%v factB=%v nowB=%v",
			factBadA.OccurredAt, nowA.UTC(), factBadB.OccurredAt, nowB.UTC())
	}
}
