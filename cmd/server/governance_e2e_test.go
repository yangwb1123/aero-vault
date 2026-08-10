package main

// Activation-gate scope-alignment matrix E2E (test-only; design
// docs/requirements/activation-gate-scope-alignment-matrix-e2e-v2.design.md).
// Pins, against real FileService+EventBus+Repository wiring (main.go order):
//   - REQ-1: bound-tenant first PUT -> exactly one outbox row + exactly one POST
//   - REQ-2: unbound tenant -> zero outbox rows, zero POSTs, zero token calls
//   - REQ-3: matrix M1-M6 exact terminal outbox state (delivered / 409 / 422 /
//     tenant-mismatch / transient-200 / conflict receipt)
//   - T-3 pins: 202-only acceptance, 200 transient, full 4-member permanent
//     closed list (relay.go:228-236), deterministic fact-ID recomputation
//   - REQ-4 behavioral pins: POST path+query, OAuth2 form scope/resource,
//     response-scope echo (a RequiredScope drift fails M1 loudly)
//
// Zero production/schema/dependency footprint. All SQL uses sqlite "?"
// placeholders (I1); created_at is TEXT RFC3339 (ms) and parsed RFC3339Nano,
// byte-identical to the write path's flexTime scan.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

const (
	e2eClientID     = "e2e-client"
	e2eClientSecret = "e2e-secret-0000"
	e2eToken        = "e2e-token"
	e2eTenant       = "acme"
)

// ── fake receiver ──────────────────────────────────────────────────────────

type govPost struct {
	eventID string
	at      time.Time
	authz   string
}

type govReceiver struct {
	server     *httptest.Server
	mode       string // 202-echo | 202-conflict | 409 | 422 | 200-then-202 | 202-wrong-tenant
	mu         sync.Mutex
	posts      []govPost
	source     string // source_system captured from the first POST body
	postCount  atomic.Int64
	tokenCalls atomic.Int64
}

func newGovReceiver(mode string) *govReceiver {
	receiver := &govReceiver{mode: mode}
	receiver.server = httptest.NewServer(http.HandlerFunc(receiver.serve))
	return receiver
}

func (r *govReceiver) serve(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/token" {
		r.tokenCalls.Add(1)
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		// Hard pins (REQ-4): token.go:64 always passes RequiredScope and the
		// resourceTransport injects RequiredResource, so these are unconditional.
		if req.Form.Get("grant_type") != "client_credentials" ||
			req.Form.Get("scope") != auditgovernance.RequiredScope ||
			req.Form.Get("resource") != auditgovernance.RequiredResource {
			http.Error(w, "token form mismatch", http.StatusBadRequest)
			return
		}
		user, secret, ok := req.BasicAuth()
		if !ok || user != e2eClientID || secret != e2eClientSecret {
			http.Error(w, "bad client credentials", http.StatusUnauthorized)
			return
		}
		// Snake_case OAuth2 wire shape (snaplink wireTokenResponse) + scope echo:
		// validTokenScopes exact-matches RequiredScope (token.go:152-153).
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+e2eToken+`","token_type":"Bearer",`+
			`"expires_in":3600,"scope":"`+auditgovernance.RequiredScope+`"}`)
		return
	}
	if req.URL.Path != "/api/v1/events" {
		http.NotFound(w, req)
		return
	}
	if req.URL.RawQuery != "wait_for=ledgered" { // model.go:19 + http.go:36-39 pin
		http.Error(w, "unexpected query", http.StatusBadRequest)
		return
	}
	var body struct {
		EventID      string `json:"event_id"`
		SourceSystem string `json:"source_system"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	at := time.Now().UTC()
	r.mu.Lock()
	r.posts = append(r.posts, govPost{eventID: body.EventID, at: at,
		authz: req.Header.Get("Authorization")})
	if r.source == "" {
		r.source = body.SourceSystem
	}
	sequence := len(r.posts) - 1
	r.mu.Unlock()
	r.postCount.Add(1)
	switch r.mode {
	case "409":
		http.Error(w, "conflict", http.StatusConflict)
	case "422":
		http.Error(w, "unprocessable", http.StatusUnprocessableEntity)
	case "200-then-202":
		if sequence == 0 {
			http.Error(w, "deferred", http.StatusOK)
			return
		}
		r.writeReceipt(w, body.EventID, e2eTenant, false)
	case "202-conflict":
		r.writeReceipt(w, body.EventID, e2eTenant, true)
	case "202-wrong-tenant":
		// The wire governanceEvent has no tenant_id; the echo is script-constant.
		r.writeReceipt(w, body.EventID, "mallory", false)
	default: // 202-echo
		r.writeReceipt(w, body.EventID, e2eTenant, false)
	}
}

func (r *govReceiver) writeReceipt(w http.ResponseWriter, eventID, tenant string, conflict bool) {
	// Explicit application/json: a missing Content-Type on a 202 is itself
	// ErrInvalidReceipt (http.go:178-185).
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":%q,"tenant_id":%q,"status":"ledgered",`+
		`"accepted_at":%q,"conflict":%t,"duplicate":false}}`,
		eventID, tenant, time.Now().UTC().Format(time.RFC3339Nano), conflict)
}

// sourceSystem is the mutex-guarded read of r.source: the field is written
// in serve under r.mu, and unlocked reads (previously direct field access in
// the wantFactID call sites) are a formal Go-memory-model violation even when
// TSan's happens-before chains mask them. cmd/server runs under -race via the
// extended test-race target, so this accessor is the gate-clean access path.
func (r *govReceiver) sourceSystem() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source
}

func (r *govReceiver) firstPost() govPost {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.posts) == 0 {
		return govPost{}
	}
	return r.posts[0]
}

func (r *govReceiver) postDelta() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.posts) < 2 {
		return 0
	}
	return r.posts[1].at.Sub(r.posts[0].at)
}

// governanceE2EConfig is the single source of the harness timing envelope
// (refactor B: the literal previously inlined in newGovernanceE2E). It
// returns a complete, valid envelope — fixed loopback endpoints keep it
// usable standalone by boot-gate tests that must not dial a live receiver;
// live harnesses override BaseURL/TokenURL with their receiver endpoint.
func governanceE2EConfig() config.AuditGovernanceConfig {
	return config.AuditGovernanceConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:1", TokenURL: "http://127.0.0.1:1/token",
		HMACKey:            "0123456789abcdef0123456789abcdef", // 32 B, distinct from secrets
		HTTPTimeoutSeconds: 5, PollMilliseconds: 5, BatchSize: 16,
		ClaimTTLSeconds: 30, InitialBackoffSeconds: 1, MaxBackoffSeconds: 2,
		MaxLagSeconds: 60, ReconcileBatchSize: 8,
		DeliveredRetentionSeconds: 3600, CleanupIntervalSeconds: 60, CleanupBatchSize: 100,
		Revision: 1,
		Bindings: []config.AuditGovernanceBinding{{
			TenantID: e2eTenant, ClientID: e2eClientID, State: "active",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E", // required by validAuditSecretEnv
			ClientSecret:    e2eClientSecret,
		}},
	}
}

// ── harness (main.go order; relay deliberately unstarted) ──────────────────

type govHarness struct {
	svc      *service.FileService
	dsn      string
	receiver *govReceiver
	rt       *auditgovernance.Runtime
}

func newGovernanceE2E(t *testing.T, mode string) *govHarness {
	t.Helper()
	receiver := newGovReceiver(mode)
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "e2e.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := governanceE2EConfig()
	cfg.BaseURL = receiver.server.URL
	cfg.TokenURL = receiver.server.URL + "/token"
	rt, err := auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)
	if err != nil {
		t.Fatal(err)
	}
	wrepo := auditgovernance.WrapRepository(repo, rt)
	bus := events.New(wrepo, logger)
	bus.WithRepository(wrepo)
	svc := service.NewFileService(store, wrepo, logger).WithEventSink(bus)
	// Cleanup runs last-registered first: rt.Close -> repo.Close -> server.Close.
	t.Cleanup(receiver.server.Close)
	t.Cleanup(func() { _ = repo.Close() })
	t.Cleanup(rt.Close)
	return &govHarness{svc: svc, dsn: dsn, receiver: receiver, rt: rt}
}

func putObject(t *testing.T, svc *service.FileService, tenant, key string) repository.Object {
	t.Helper()
	obj, err := svc.Put(context.Background(), tenant, "default", key,
		strings.NewReader("x"), 1, service.PutOptions{})
	if err != nil {
		t.Fatalf("put %s/%s: %v", tenant, key, err)
	}
	return obj
}

func startRelay(t *testing.T, rt *auditgovernance.Runtime) {
	t.Helper()
	rt.Start(context.Background()) // Background: T.Context cancels before Cleanup
}

// ── assertion helpers (raw sqlite conns; "?" placeholders per I1) ──────────

// eventRowID resolves the outbox origin_id: the object_events row id (a
// separate AUTOINCREMENT from objects.id — A4), via the object FK.
func eventRowID(t *testing.T, dsn string, objectID int64) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM object_events WHERE object_id=? ORDER BY id DESC LIMIT 1`,
		objectID).Scan(&id); err != nil {
		t.Fatalf("event row: %v", err)
	}
	return id
}

type govOutboxRow struct {
	id            string
	tenantID      string
	originKind    string
	originID      int64
	attempts      int
	availableAtNS int64
	claimOwner    string
	claimToken    string
	lastError     string
	deliveredAtNS int64
	failedAtNS    int64
	leaseExpires  int64
}

func outboxRow(t *testing.T, dsn string, originID int64) (govOutboxRow, error) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var row govOutboxRow
	err = db.QueryRow(`SELECT id,tenant_id,origin_kind,origin_id,attempts,
available_at_ns,claim_owner,claim_token,last_error,delivered_at_ns,failed_at_ns,
lease_expires_at_ns FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=?`,
		originID).Scan(&row.id, &row.tenantID, &row.originKind, &row.originID,
		&row.attempts, &row.availableAtNS, &row.claimOwner, &row.claimToken,
		&row.lastError, &row.deliveredAtNS, &row.failedAtNS, &row.leaseExpires)
	return row, err
}

// wantFactID recomputes the deterministic fact ID from observed inputs only:
// the wire source_system + the object_events row (id, type, created_at).
// A drift in any formula input breaks the pin loudly (E13/T-4, H2/R2).
func wantFactID(t *testing.T, dsn, source string, objectID int64) string {
	t.Helper()
	if !strings.HasPrefix(source, "aero-vault.") || len(source) != 54 {
		t.Fatalf("source shape pin: prefix/len got %q (%d)", source, len(source))
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rowID int64
	var eventType, createdRaw string
	if err := db.QueryRow(
		`SELECT id,type,created_at FROM object_events WHERE object_id=? ORDER BY id DESC LIMIT 1`,
		objectID).Scan(&rowID, &eventType, &createdRaw); err != nil {
		t.Fatalf("event row: %v", err)
	}
	occurred, err := time.Parse(time.RFC3339Nano, createdRaw) // flexTime layout order
	if err != nil {
		t.Fatalf("parse created_at %q: %v", createdRaw, err)
	}
	return repository.DeterministicFactID(source, e2eTenant, "file."+eventType,
		repository.AuditOriginFile, rowID, occurred)
}

func waitForRow(t *testing.T, dsn string, originID int64, pred func(govOutboxRow) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last govOutboxRow
	for time.Now().Before(deadline) {
		last, _ = outboxRow(t, dsn, originID)
		if pred(last) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("row never reached desired state: last=%+v", last)
}

// quiesce asserts a negative/stability property for a full window; negative
// assertions must never use waitFor (true at t=0 is vacuous — A6).
func quiesce(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatal("quiesce violated")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func rowFor(t *testing.T, h *govHarness, originID int64) govOutboxRow {
	t.Helper()
	row, err := outboxRow(t, h.dsn, originID)
	if err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	return row
}

// ── REQ-1: activation gate, bound tenant ───────────────────────────────────

func TestGovernanceE2EActivationGateBoundTenant(t *testing.T) {
	h := newGovernanceE2E(t, "202-echo")
	obj := putObject(t, h.svc, e2eTenant, "gate.txt")
	originID := eventRowID(t, h.dsn, obj.ID)
	// Deterministic pre-start snapshot (B1): relay not started, no claim possible.
	row := rowFor(t, h, originID)
	if row.tenantID != e2eTenant || row.originKind != repository.AuditOriginFile ||
		row.originID != originID || row.attempts != 0 || row.deliveredAtNS != 0 ||
		row.failedAtNS != 0 || row.availableAtNS == 0 || row.claimOwner != "" {
		t.Fatalf("pre-start snapshot: %+v", row)
	}
	startRelay(t, h.rt)
	waitForRow(t, h.dsn, originID, func(row govOutboxRow) bool {
		return row.deliveredAtNS > 0 && row.attempts == 1 &&
			row.claimOwner == "" && row.lastError == ""
	})
	quiesce(t, 50*time.Millisecond, func() bool { return h.receiver.postCount.Load() == 1 })
	if calls := h.receiver.tokenCalls.Load(); calls != 1 {
		t.Fatalf("tokenCalls=%d want 1", calls)
	}
	first := h.receiver.firstPost()
	if first.eventID != rowFor(t, h, originID).id {
		t.Fatalf("POST event_id=%q != outbox id %q", first.eventID, rowFor(t, h, originID).id)
	}
	if first.authz != "Bearer "+e2eToken {
		t.Fatalf("POST Authorization=%q", first.authz)
	}
	if id := rowFor(t, h, originID).id; id != wantFactID(t, h.dsn, h.receiver.sourceSystem(), obj.ID) {
		t.Fatalf("fact ID %q != deterministic recomputation", id)
	}
}

// ── REQ-2: activation gate, unbound tenant ─────────────────────────────────

func TestGovernanceE2EActivationGateUnboundTenant(t *testing.T) {
	h := newGovernanceE2E(t, "202-echo")
	obj := putObject(t, h.svc, "other", "gate.txt")
	originID := eventRowID(t, h.dsn, obj.ID) // gate-1 fallthrough: event row exists
	if _, err := outboxRow(t, h.dsn, originID); err == nil {
		t.Fatal("unbound tenant produced an outbox row")
	} else if err != sql.ErrNoRows {
		t.Fatalf("outbox query: %v", err)
	}
	startRelay(t, h.rt)
	quiesce(t, 1*time.Second, func() bool {
		return h.receiver.postCount.Load() == 0 && h.receiver.tokenCalls.Load() == 0
	})
}

// ── REQ-3 matrix: M1 delivered ─────────────────────────────────────────────

func TestGovernanceE2EMatrixDelivered(t *testing.T) {
	h := newGovernanceE2E(t, "202-echo")
	obj := putObject(t, h.svc, e2eTenant, "delivered.txt")
	originID := eventRowID(t, h.dsn, obj.ID)
	startRelay(t, h.rt)
	waitForRow(t, h.dsn, originID, func(row govOutboxRow) bool {
		return row.deliveredAtNS > 0 && row.failedAtNS == 0 && row.attempts == 1 &&
			row.lastError == "" && row.claimOwner == ""
	})
	quiesce(t, 50*time.Millisecond, func() bool { return h.receiver.postCount.Load() == 1 })
	if id := rowFor(t, h, originID).id; id != wantFactID(t, h.dsn, h.receiver.sourceSystem(), obj.ID) {
		t.Fatalf("fact ID %q != deterministic recomputation", id)
	}
}

// ── REQ-3 matrix M2/M3/M4 + REQ-5/T-3 M6: permanent classes ────────────────

func TestGovernanceE2EMatrixPermanentClasses(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		errSubstr string // substring of the actual sentinel (R1/R6)
	}{
		{"M2Conflict409", "409", "audit governance HTTP 409"},
		{"M3Unprocessable422", "422", "audit governance HTTP 422"},
		{"M4TenantMismatch", "202-wrong-tenant", "receipt is invalid"},
		{"M6ConflictReceipt", "202-conflict", "reports a conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGovernanceE2E(t, tc.mode)
			obj := putObject(t, h.svc, e2eTenant, "perm.txt")
			originID := eventRowID(t, h.dsn, obj.ID)
			startRelay(t, h.rt)
			waitForRow(t, h.dsn, originID, func(row govOutboxRow) bool {
				return row.failedAtNS > 0 && row.deliveredAtNS == 0 && row.attempts == 1 &&
					strings.Contains(row.lastError, tc.errSubstr)
			})
			// Terminal: single POST, no retry — the claim predicate requires
			// failed_at_ns=0, so re-claim is impossible (belt-and-braces).
			quiesce(t, 50*time.Millisecond, func() bool { return h.receiver.postCount.Load() == 1 })
		})
	}
}

// ── REQ-3 matrix M5: 200 is transient (bounded-backoff retry) ──────────────

func TestGovernanceE2EMatrixTransient200(t *testing.T) {
	h := newGovernanceE2E(t, "200-then-202")
	obj := putObject(t, h.svc, e2eTenant, "transient.txt")
	originID := eventRowID(t, h.dsn, obj.ID)
	startRelay(t, h.rt)
	// Stage 1: transient state persists >= 750 ms (boundedBackoff 1s +- 25%,
	// relay.go:181-197); both asserted timestamps are fixed once retryFact
	// commits — no wall-clock reads (B2/R3).
	waitForRow(t, h.dsn, originID, func(row govOutboxRow) bool {
		return h.receiver.postCount.Load() == 1 && row.lastError != "" &&
			row.deliveredAtNS == 0 && row.failedAtNS == 0
	})
	row := rowFor(t, h, originID)
	if gap := row.availableAtNS - h.receiver.firstPost().at.UnixNano(); gap < 700*int64(time.Millisecond) {
		t.Fatalf("backoff window too short: available_at-POST1 = %d ns", gap)
	}
	// Stage 2: retried (attempts=2 via re-claim) and delivered.
	waitForRow(t, h.dsn, originID, func(row govOutboxRow) bool {
		return row.deliveredAtNS > 0 && row.attempts == 2 && row.lastError == ""
	})
	if id := rowFor(t, h, originID).id; id != wantFactID(t, h.dsn, h.receiver.sourceSystem(), obj.ID) {
		t.Fatalf("fact ID %q != deterministic recomputation", id)
	}
	quiesce(t, 50*time.Millisecond, func() bool {
		if h.receiver.postCount.Load() != 2 {
			return false
		}
		return h.receiver.postDelta() >= 700*time.Millisecond // gated by available_at_ns
	})
}
