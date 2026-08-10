package main

// B3-5: inbound audit:event:write scope gate on the HTTP /mcp mount (design
// docs/auto/designs/internal-mcp-inbound-scope-gate-b3-5.md; spec
// docs/auto/specs/internal-mcp-inbound-scope-gate-b3-5.md). Pins, against the
// real 12-ring chain + gate + MCP dispatch (main.go assembly order):
//   - REQ-1/REQ-4 (T-1): a bearer provisioned with write + audit:event:write
//     (Registry.AddKey — knownScope blocks AUTH_KEYS/SigV4/Snaplink from
//     carrying the scope, E9) drives tools/call write_file through the gate;
//     the object lands in the key's tenant, the outbox B1 pre-start snapshot
//     holds, and the relay delivers exactly one POST with Bearer e2e-token,
//     event_id == outbox id == wantFactID recomputation, exactly one token
//     call (REQ-4 form pins inherited from govReceiver's /token handler).
//   - REQ-1/REQ-5 (T-2): a write-scoped bearer without audit:event:write gets
//     HTTP 403 with byte-exact body "missing scope: audit:event:write\n" and
//     no WWW-Authenticate — discriminated from the chain's coarse gate
//     ("missing scope: write"); zero outbox/object_events/objects rows, zero
//     receiver POSTs and token calls over a quiesce window; unauthenticated
//     POST → 401 (Auth ring, runs before the gate) with the same
//     zero-side-effect property; read tools are gated too; admin keys keep
//     /mcp (Key.Has implies).
//   - REQ-2: the mount (cmd/server/http.go) and this file share the
//     mcpScopeGate symbol — drift is compile-time impossible for the helper.
//
// Zero production/schema/dependency footprint. All raw SQL uses sqlite "?"
// placeholders (I1). No quoted literal of the RequiredScope value anywhere in
// this file (REQ-3): provisioning keys and the 403 assertion compose from
// auditgovernance.RequiredScope.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/mcp"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/server"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

const (
	mcpWritePayload = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"key":"mcp-gate.txt","content":"gate"}}}`
	mcpListPayload  = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
)

// mcpGovHarness embeds the shared govHarness (rowFor/waitForRow/eventRowID/
// outboxRow/wantFactID read h.dsn; startRelay reads h.rt; the receiver is
// h.receiver) so the B3-5 tests reuse the governance_e2e_test.go helpers
// read-only — that file stays untouched at exactly 500 lines.
type mcpGovHarness struct {
	govHarness
	url string
}

// newMCPGovernanceE2E mirrors newGovernanceE2E (main.go construction order,
// relay deliberately unstarted) plus the MCP adapter, the mcpScopeGate mount
// (REQ-2: same symbol as production), and the real 12-ring chain via
// server.ApplyMiddleware (main.go:166). All nil/zero members are verified
// pass-through (E12): auth.Parse("") disabled registry, nil rate limiter,
// zero config, unlimited concurrency limiter.
func newMCPGovernanceE2E(t *testing.T, mode string) *mcpGovHarness {
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
	cfg := config.AuditGovernanceConfig{
		Enabled: true, BaseURL: receiver.server.URL, TokenURL: receiver.server.URL + "/token",
		HMACKey:            "0123456789abcdef0123456789abcdef", // 32 B, distinct from secrets
		HTTPTimeoutSeconds: 5, PollMilliseconds: 5, BatchSize: 16,
		ClaimTTLSeconds: 30, InitialBackoffSeconds: 1, MaxBackoffSeconds: 2,
		MaxLagSeconds: 60, ReconcileBatchSize: 8,
		DeliveredRetentionSeconds: 3600, CleanupIntervalSeconds: 60, CleanupBatchSize: 100,
		Revision: 1,
		Bindings: []config.AuditGovernanceBinding{{
			TenantID: e2eTenant, ClientID: e2eClientID, State: "active",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E",
			ClientSecret:    e2eClientSecret,
		}},
	}
	rt, err := auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)
	if err != nil {
		t.Fatal(err)
	}
	wrepo := auditgovernance.WrapRepository(repo, rt)
	bus := events.New(wrepo, logger)
	bus.WithRepository(wrepo)
	svc := service.NewFileService(store, wrepo, logger).WithEventSink(bus)
	authReg, err := auth.Parse("")
	if err != nil {
		t.Fatal(err)
	}
	// Provisioning (E9): knownScope rejects the audit scope from
	// AUTH_KEYS/SigV4/Snaplink; Registry.AddKey accepts arbitrary scopes.
	// All three principals are registered up front so T-1 and T-2 share the
	// harness; mcp-admin pins the admin-implies clause (T-2 step 5).
	addMCPKey(t, authReg, "mcp-write", e2eTenant, auth.ScopeWrite, auth.Scope(auditgovernance.RequiredScope))
	addMCPKey(t, authReg, "mcp-readonly", e2eTenant, auth.ScopeWrite)
	addMCPKey(t, authReg, "mcp-admin", e2eTenant, auth.ScopeAdmin)
	mcpServer := mcp.NewServer(svc, wrepo, nil, "default", logger)
	router := chi.NewRouter()
	router.Method(http.MethodPost, "/mcp", mcpScopeGate(authReg)(mcp.HTTPHandler(mcpServer)))
	handler := server.ApplyMiddleware(router, wrepo, authReg, nil, &config.Config{}, logger,
		middleware.NewConcurrencyLimiter(0).Middleware(), nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	// Cleanup runs last-registered first: rt.Close -> repo.Close -> receiver.Close -> srv.Close.
	t.Cleanup(receiver.server.Close)
	t.Cleanup(func() { _ = repo.Close() })
	t.Cleanup(rt.Close)
	return &mcpGovHarness{govHarness: govHarness{dsn: dsn, receiver: receiver, rt: rt}, url: srv.URL}
}

func addMCPKey(t *testing.T, reg *auth.Registry, token, tenant string, scopes ...auth.Scope) {
	t.Helper()
	key := auth.Key{Token: token, Tenant: tenant, Scopes: make(map[auth.Scope]bool)}
	for _, s := range scopes {
		key.Scopes[s] = true
	}
	if err := reg.AddKey(context.Background(), key, "", ""); err != nil {
		t.Fatalf("AddKey(%s): %v", token, err)
	}
}

// ── HTTP / MCP wire helpers ────────────────────────────────────────────────

type mcpResp struct {
	status int
	header http.Header
	body   string
}

func postMCP(t *testing.T, h *mcpGovHarness, bearer, payload string) mcpResp {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.url+"/mcp", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return mcpResp{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

// toolResultText decodes the JSON-RPC envelope of a 200 response and returns
// the first text content block of a tools/call result.
func toolResultText(t *testing.T, resp mcpResp) string {
	t.Helper()
	if resp.status != http.StatusOK {
		t.Fatalf("status=%d want 200 (body %q)", resp.status, resp.body)
	}
	var envelope struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp.body), &envelope); err != nil {
		t.Fatalf("decode %q: %v", resp.body, err)
	}
	if envelope.Error != nil {
		t.Fatalf("rpc error: %+v", envelope.Error)
	}
	if envelope.Result == nil || len(envelope.Result.Content) == 0 {
		t.Fatalf("no result content: %q", resp.body)
	}
	return envelope.Result.Content[0].Text
}

func countRows(t *testing.T, h *mcpGovHarness, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func objectIDByKey(t *testing.T, h *mcpGovHarness, tenant, bucket, key string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id int64
	if err := db.QueryRow(`SELECT id FROM objects WHERE tenant_id=? AND bucket=? AND key=? ORDER BY id DESC LIMIT 1`,
		tenant, bucket, key).Scan(&id); err != nil {
		t.Fatalf("object %s/%s/%s: %v", tenant, bucket, key, err)
	}
	return id
}

// assertZeroSideEffects pins REQ-5: the gate rejects before dispatch, so no
// outbox row, no object_events row, no objects row, and no receiver traffic
// over a quiesce window (1s = 200x the 5ms poll — a stability assertion,
// never vacuous: T-1 proves the same harness delivers when admitted).
func assertZeroSideEffects(t *testing.T, h *mcpGovHarness) {
	t.Helper()
	for _, table := range []string{"audit_governance_outbox", "object_events", "objects"} {
		if n := countRows(t, h, table); n != 0 {
			t.Fatalf("denied request produced %d rows in %s", n, table)
		}
	}
	quiesce(t, 1*time.Second, func() bool {
		return h.receiver.postCount.Load() == 0 && h.receiver.tokenCalls.Load() == 0
	})
}

// ── T-1 (REQ-4 / A1): provisioned bearer -> fact delivered ────────────────

func TestGovernanceE2EMCPWriteFileProvisionedBearer(t *testing.T) {
	h := newMCPGovernanceE2E(t, "202-echo")

	// Gate admits the provisioned scope: the write lands and the tool result
	// echoes the exact toolWriteFile format (internal/mcp/server.go).
	resp := postMCP(t, h, "mcp-write", mcpWritePayload)
	if got := toolResultText(t, resp); !strings.Contains(got, "written: mcp-gate.txt (4 bytes)") {
		t.Fatalf("tool result %q", got)
	}

	// The object exists under the key's tenant (Auth ring normalized the
	// tenant header to acme end-to-end) and produced exactly one event.
	objID := objectIDByKey(t, h, e2eTenant, "default", "mcp-gate.txt")
	originID := eventRowID(t, h.dsn, objID)

	// Deterministic pre-start snapshot (B1): relay not started, no claim.
	row := rowFor(t, &h.govHarness, originID)
	if row.tenantID != e2eTenant || row.originKind != repository.AuditOriginFile ||
		row.attempts != 0 || row.deliveredAtNS != 0 || row.failedAtNS != 0 ||
		row.availableAtNS == 0 || row.claimOwner != "" {
		t.Fatalf("pre-start snapshot: %+v", row)
	}

	// Relay delivers exactly once with the REQ-4 form pins.
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
	if first.eventID != rowFor(t, &h.govHarness, originID).id {
		t.Fatalf("POST event_id=%q != outbox id %q", first.eventID, rowFor(t, &h.govHarness, originID).id)
	}
	if first.authz != "Bearer "+e2eToken {
		t.Fatalf("POST Authorization=%q", first.authz)
	}
	if id := rowFor(t, &h.govHarness, originID).id; id != wantFactID(t, h.dsn, h.receiver.sourceSystem(), objID) {
		t.Fatalf("fact ID %q != deterministic recomputation", id)
	}
}

// ── T-2 (REQ-5 / A2): rejection precedes enqueue ──────────────────────────

func TestGovernanceE2EMCPScopeGateDeniesUnprovisioned(t *testing.T) {
	h := newMCPGovernanceE2E(t, "202-echo")

	// Step 1: write-scoped bearer without audit:event:write -> 403 from the
	// mount gate, byte-exact body, no WWW-Authenticate. The coarse chain gate
	// can never emit this body (it would say "missing scope: write" — and
	// mcp-readonly holds write, so it passes the ring), which makes the
	// discriminator sound at wire level.
	resp := postMCP(t, h, "mcp-readonly", mcpWritePayload)
	if resp.status != http.StatusForbidden {
		t.Fatalf("status=%d want 403 (body %q)", resp.status, resp.body)
	}
	if got := resp.header.Get("WWW-Authenticate"); got != "" {
		t.Fatalf("403 carried WWW-Authenticate: %q", got)
	}
	if ct := resp.header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("403 Content-Type=%q", ct)
	}
	if want := "missing scope: " + auditgovernance.RequiredScope + "\n"; resp.body != want {
		t.Fatalf("403 body=%q want %q", resp.body, want)
	}

	// Step 2: rejection precedes enqueue — zero rows, zero receiver traffic.
	assertZeroSideEffects(t, h)

	// Step 3: unauthenticated POST -> 401 from the Auth ring (runs before
	// the gate), with the standard challenge; same zero side effects.
	noAuth := postMCP(t, h, "", mcpWritePayload)
	if noAuth.status != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 (body %q)", noAuth.status, noAuth.body)
	}
	if got := noAuth.header.Get("WWW-Authenticate"); got != `Bearer realm="aero-vault"` {
		t.Fatalf("401 WWW-Authenticate=%q", got)
	}
	if noAuth.body != "missing Authorization header\n" {
		t.Fatalf("401 body=%q", noAuth.body)
	}
	assertZeroSideEffects(t, h)

	// Steps 4-5 (discriminator + admin clause): the gate admits the
	// provisioned scope and admin-implies; read-only tools are gated too.
	for _, tc := range []struct {
		bearer string
		want   int
	}{
		{"mcp-write", http.StatusOK},
		{"mcp-readonly", http.StatusForbidden},
		{"mcp-admin", http.StatusOK},
	} {
		got := postMCP(t, h, tc.bearer, mcpListPayload)
		if got.status != tc.want {
			t.Fatalf("tools/list %s: status=%d want %d (body %q)", tc.bearer, got.status, tc.want, got.body)
		}
	}
}
