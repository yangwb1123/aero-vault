package s3compat

// B3-3 deterministic fact IDs — GAP-1: the s3compat end-to-end pin.
//
// Production-shaped wiring (mirrors cmd/server/main.go:79-86): the repo is
// wrapped by auditgovernance.WrapRepository, the bus persists through the
// wrapped repo (WithRepository), and FileService emits into that bus
// (WithEventSink). A PUT then flows PutObject → bus.Publish → wrapped
// InsertEvent → InsertEventWithGovernance, which mints the store-authoritative
// deterministic fact ID (RETURNING id, created_at + DeterministicFactID).
//
// The "exactly 1 outbox row after PUT" assertion is the ONLY detector for a
// broken event path (F5): Bus.Publish swallows persistence errors by design,
// so a broken wiring yields HTTP 200 + zero rows.
//
// No Runtime.Start(), no receiver traffic, no live clock in expected-ID math
// (F9): every expected timestamp is read back from the DB row.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

const e2eGovernanceTenant = "default"

// e2eFactIDPattern is the campaign's fact-ID shape: 32 lowercase hex chars.
var e2eFactIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// e2eSourceID is a byte-for-byte test-local replica of the production
// redactor.tenantSourceID (redaction.go): same domain string, field order, NUL
// terminator convention, prefix, and base64 variant. It is the only external
// pin on source-derivation framing (F1) — a drift in any of those breaks the
// golden anchors D/E and the row recompute below.
func e2eSourceID(key, tenant string) string {
	mac := hmac.New(sha256.New, []byte(key))
	for _, field := range []string{"aero-vault/audit-governance/v1", tenant, "source-system", tenant} {
		mac.Write([]byte(field))
		mac.Write([]byte{0})
	}
	return "aero-vault." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// newGovernanceE2EServer delegates to newGovernanceE2EServerWithAuthz with
// the historical allowAllProvider{} assembly — both existing callers
// (DeterministicFactID, CaptureInactive) keep the exact signature and
// semantics byte-identical.
func newGovernanceE2EServer(t *testing.T, bindingState string) (*httptest.Server, repository.AuditGovernanceStore, string) {
	t.Helper()
	return newGovernanceE2EServerWithAuthz(t, bindingState, allowAllProvider{})
}

// newGovernanceE2EServerWithAuthz is the production-shaped governance wiring
// over a fresh sqlite repo (FR-5) with an injectable adapter gate provider.
// The router third argument is the ONLY injection point —
// NewRouter(svc, nil, authz) mirrors production assembly (router.go:14-15);
// the service-side authorizer stays allowAllProvider{} (the REST-side
// baseline, independent of the adapter gate). Fixture attribution: flipping
// either side self-neutralizes the Gate detector — service-side deny still
// yields 403+zero rows (from the service gate) while silently skipping the
// adapter gate; router-side allow leaks the delete. bindingState "active"
// captures, "draining" does not (F4 negative). Loopback BaseURL/TokenURL pass
// config validation and are never dialed (Runtime is never Start()ed — no
// goroutines, deterministic).
func newGovernanceE2EServerWithAuthz(t *testing.T, bindingState string, authz AuthorizationProvider) (*httptest.Server, repository.AuditGovernanceStore, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "gov.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	govStore := repo.(repository.AuditGovernanceStore)
	cfg := config.AuditGovernanceConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:9", TokenURL: "http://127.0.0.1:9/token",
		HMACKey: string(testShareSecret), HTTPTimeoutSeconds: 1, PollMilliseconds: 10,
		BatchSize: 10, ClaimTTLSeconds: 3, InitialBackoffSeconds: 1, MaxBackoffSeconds: 2,
		MaxLagSeconds: 4, ReconcileBatchSize: 20, DeliveredRetentionSeconds: 3600,
		CleanupIntervalSeconds: 60, CleanupBatchSize: 20, Revision: 1,
		Bindings: []config.AuditGovernanceBinding{{TenantID: e2eGovernanceTenant,
			ClientID: "vault-e2e", ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E",
			ClientSecret: "e2e-secret", State: bindingState}},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime, err := auditgovernance.New(cfg, govStore, logger)
	if err != nil {
		t.Fatalf("governance runtime: %v", err)
	}
	t.Cleanup(runtime.Close)
	wrapped := auditgovernance.WrapRepository(repo, runtime)
	bus := events.New(wrapped, logger).WithRepository(wrapped)
	svc := service.NewFileService(store, wrapped, logger).
		WithEventSink(bus).WithAuthorizer(allowAllProvider{})
	srv := httptest.NewServer(NewRouter(svc, nil, authz))
	t.Cleanup(srv.Close)
	return srv, govStore, dsn
}

// governanceOutboxRowForAction is the type-filtered variant of
// governanceOutboxRow (Finding A): after PUT+DELETE the same bucket/key
// matches BOTH the file.created and file.deleted rows, so an unfiltered
// QueryRow returns an unspecified row (scan order → likely the lower-rowid
// file.created), producing spurious reID-mismatch or wrong-action failures.
// EVERY delete-row read — including the T-4.2 post-re-enqueue byte-identical
// re-read — MUST go through this variant; the unfiltered helper is reserved
// for counts/found-agnostic checks (capture-inactive semantics). count is the
// row count for that action (FM-5 duplicate guard).
func governanceOutboxRowForAction(t *testing.T, dsn, bucket, key, action string) (
	found bool, id string, originID int64, occurredNS int64, actionGot, tenantID, createdRaw string, count int,
) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open outbox db: %v", err)
	}
	defer db.Close()
	row := db.QueryRow(`SELECT o.id, o.origin_id, o.action, o.tenant_id, o.occurred_at_ns, e.created_at
FROM audit_governance_outbox o JOIN object_events e ON e.id = o.origin_id
WHERE o.origin_kind='file' AND o.tenant_id='default' AND e.bucket=? AND e.key=? AND o.action=?`, bucket, key, action)
	switch err := row.Scan(&id, &originID, &actionGot, &tenantID, &occurredNS, &createdRaw); err {
	case nil:
		found = true
	case sql.ErrNoRows:
	default:
		t.Fatalf("scan outbox row: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_governance_outbox WHERE action=?`, action).Scan(&count); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return found, id, originID, occurredNS, actionGot, tenantID, createdRaw, count
}

// governanceOutboxRow reads the outbox row for the PUT object via a second
// sqlite connection. found=false means no outbox row exists (capture inactive
// or broken event path); count is the whole-table outbox row count. Do NOT use
// this for delete-flow reads — after PUT+DELETE the same bucket/key matches
// both rows; use governanceOutboxRowForAction instead (Finding A).
func governanceOutboxRow(t *testing.T, dsn, bucket, key string) (
	found bool, id string, originID int64, occurredNS int64, action, tenantID, createdRaw string, count int,
) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open outbox db: %v", err)
	}
	defer db.Close()
	row := db.QueryRow(`SELECT o.id, o.origin_id, o.action, o.tenant_id, o.occurred_at_ns, e.created_at
FROM audit_governance_outbox o JOIN object_events e ON e.id = o.origin_id
WHERE o.origin_kind='file' AND o.tenant_id='default' AND e.bucket=? AND e.key=?`, bucket, key)
	switch err := row.Scan(&id, &originID, &action, &tenantID, &occurredNS, &createdRaw); err {
	case nil:
		found = true
	case sql.ErrNoRows:
	default:
		t.Fatalf("scan outbox row: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_governance_outbox`).Scan(&count); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return found, id, originID, occurredNS, action, tenantID, createdRaw, count
}

func TestS3CompatAuditGovernanceDeterministicFactID(t *testing.T) {
	ctx := context.Background()
	srv, store, dsn := newGovernanceE2EServer(t, "active")

	// REQ-1 / AC-1: PUT → exactly one durable governance outbox row.
	resp, _ := do(t, "PUT", srv.URL+"/b/k.txt", []byte("hello"), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	// F5 detector (the only one): a broken event path yields HTTP 200 + 0 rows.
	found, id, originID, occurredNS, action, tenantID, createdRaw, count :=
		governanceOutboxRow(t, dsn, "b", "k.txt")
	if !found || count != 1 {
		t.Fatalf("outbox rows=%d found=%v want exactly 1 (F5 event-path detector)", count, found)
	}
	if action != "file.created" || tenantID != "default" || originID <= 0 {
		t.Fatalf("outbox row action=%q tenant=%q origin=%d", action, tenantID, originID)
	}

	// REQ-2 / F3: occurred was canonicalized to the stored origin created_at
	// (RETURNING id, created_at), never the caller's clock.
	created, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		t.Fatalf("parse origin created_at %q: %v", createdRaw, err)
	}
	if occurredNS != created.UnixNano() {
		t.Fatalf("occurred_at_ns=%d != created_at .UnixNano()=%d (REQ-2 canonicalization)", occurredNS, created.UnixNano())
	}

	// Golden D (F1 + F2): the test-local HMAC replica, absolutely pinned.
	expectedSource := e2eSourceID(string(testShareSecret), e2eGovernanceTenant)
	if expectedSource != "aero-vault.PE5txdoOQd0AhKXa_qH1g8c0l6kCKdGEPJpRNVqi1E8" {
		t.Fatalf("source replica drifted: %q (F1 framing pin)", expectedSource)
	}
	// Golden E (F2, clock-free): replica + formula combination.
	if got := repository.DeterministicFactID(expectedSource, "default", "file.created", "file", 1,
		time.Date(2026, 8, 8, 1, 17, 41, 0, time.UTC)); got != "3494289b9f82a731f3022b534a8b01de" {
		t.Fatalf("golden E drifted: %q", got)
	}

	// REQ-3 / AC-1: absolute row recompute — the stored ID equals the formula
	// over the row's own fields.
	expectedID := repository.DeterministicFactID(expectedSource, "default",
		"file.created", "file", originID, created)
	if id != expectedID || !e2eFactIDPattern.MatchString(id) {
		t.Fatalf("outbox id=%q want recompute=%q (store-authoritative determinism)", id, expectedID)
	}

	// F10: wire identity — the claim returns the pinned outbox ID.
	claimed, err := store.ClaimAuditGovernance(ctx, "e2e-owner", "e2e-token", 1, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v want=1", claimed, err)
	}
	if claimed[0].ID != id || claimed[0].OriginID != originID {
		t.Fatalf("claimed fact=%+v want id=%q origin=%d", claimed[0], id, originID)
	}

	// AC-2: prune the outbox row (the T-4 bypass: no delivered-origin
	// tombstone) → the gap path resurfaced it → re-enqueue folds to the SAME
	// byte-identical ID (receiver-side duplicate).
	prune, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open prune db: %v", err)
	}
	defer prune.Close()
	if _, err := prune.Exec(
		`DELETE FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=?`, originID); err != nil {
		t.Fatalf("prune outbox row: %v", err)
	}
	gaps, err := store.ListAuditGovernanceGaps(ctx, e2eGovernanceTenant, 10)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("gaps=%+v err=%v want=1", gaps, err)
	}
	g := gaps[0]
	if g.OriginKind != "file" || g.OriginID != originID || g.Action != "file.created" ||
		g.OccurredAt.UnixNano() != occurredNS {
		t.Fatalf("gap=%+v want kind=file origin=%d action=file.created occurred=%d", g, originID, occurredNS)
	}
	// factFromGap-equivalent reconstruction (relay.go:27→38→40): the ID is
	// recomputed store-authoritatively inside EnqueueAuditGovernance.
	rebuilt := repository.AuditGovernanceFact{
		SourceID: expectedSource, TenantID: "default", OriginKind: "file", FactKind: "file",
		Action: g.Action, OriginID: g.OriginID, OccurredAt: g.OccurredAt,
	}
	inserted, err := store.EnqueueAuditGovernance(ctx, rebuilt)
	if err != nil || !inserted {
		t.Fatalf("re-enqueue: inserted=%v err=%v", inserted, err)
	}
	found, reID, _, _, _, _, _, recount := governanceOutboxRow(t, dsn, "b", "k.txt")
	if !found || recount != 1 || reID != id {
		t.Fatalf("re-enqueued found=%v count=%d id=%q want count=1 id=%q (byte-identical AC-2)", found, recount, reID, id)
	}
	// F7: the dedupe branch folds a second enqueue to (false, nil).
	if again, err := store.EnqueueAuditGovernance(ctx, rebuilt); err != nil || again {
		t.Fatalf("duplicate enqueue inserted=%v err=%v want (false,nil)", again, err)
	}
}

// TestS3CompatAuditGovernanceCaptureInactive pins F4: with a draining binding
// the capture is inactive, so PUT must still succeed (I5 — capture never
// breaks CRUD) and store the object, persist the legacy object_events row, and
// write ZERO governance outbox rows.
func TestS3CompatAuditGovernanceCaptureInactive(t *testing.T) {
	srv, _, dsn := newGovernanceE2EServer(t, "draining")
	resp, _ := do(t, "PUT", srv.URL+"/b/k.txt", []byte("hello"), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	resp, body := do(t, "GET", srv.URL+"/b/k.txt", nil, nil)
	if resp.StatusCode != 200 || string(body) != "hello" {
		t.Fatalf("get status=%d body=%q", resp.StatusCode, body)
	}
	found, _, _, _, _, _, _, count := governanceOutboxRow(t, dsn, "b", "k.txt")
	if found || count != 0 {
		t.Fatalf("outbox rows=%d found=%v want 0 (F4 capture-inactive)", count, found)
	}
	// Distinguish inactive capture from a broken event path: the legacy
	// InsertEvent path still persisted the origin event.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open event db: %v", err)
	}
	defer db.Close()
	var eventsCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM object_events WHERE bucket='b' AND key='k.txt' AND type='created'`).Scan(&eventsCount); err != nil {
		t.Fatalf("count object_events: %v", err)
	}
	if eventsCount != 1 {
		t.Fatalf("object_events rows=%d want 1 (legacy event path must still persist)", eventsCount)
	}
}
