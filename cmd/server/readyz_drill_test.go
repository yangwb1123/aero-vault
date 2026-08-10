package main

// readyz_drill_test.go drills the /readyz HTTP seam (http.go:readyzHandler)
// against the audit-governance runtime: D1 — extra.Ready is bounded by the
// same 2s probeCtx as the storage probe; B3-2 — a backlog beyond maxLag
// degrades (200, gauge-signaled) instead of 503; B3-1 — dead-lettered rows
// and an empty store are terminal (gauge 0, 200); and the drain boundary
// still 503s. Determinism: no sleeps — timestamps are backdated via a second
// raw SQLite connection (the internal/reconcile lifecycle_test.go idiom);
// teardown is LIFO-safe (repo.Close registered first, runtime.Close second)
// and the relay is never started, so Close() is non-blocking.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// blockingReadyChecker emulates a wedged audit-governance store: Ready blocks
// on the caller context (the OldestPendingAuditGovernance hang shape) and can
// only return after the probe deadline fires.
type blockingReadyChecker struct{ readinessChecker }

func (c *blockingReadyChecker) Ready(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// errorReadyChecker fails Ready immediately with a non-context error.
type errorReadyChecker struct{ readinessChecker }

func (c *errorReadyChecker) Ready(context.Context) error { return errors.New("injected extra failure") }

// okReadyChecker reports a healthy extra dependency.
type okReadyChecker struct{ readinessChecker }

func (c *okReadyChecker) Ready(context.Context) error { return nil }

// newReadyzDrillRuntime builds a real Runtime over a real SQLite repository:
// one active acme binding (capture requires state=active, so a draining
// harness start state could never seed a pending fact), maxLag 4s > claimTTL
// 3s. The relay is never started, so Runtime.Close is non-blocking; cleanup
// registers repo.Close FIRST and runtime.Close SECOND so the LIFO execution
// order closes the runtime before the repo it queries (use-after-close-safe
// under -race).
func newReadyzDrillRuntime(t *testing.T) (*auditgovernance.Runtime, auditgovernance.Store, string) {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "drill.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := drillRuntimeConfig()
	if err := cfg.Validate(); err != nil { // MaxLag 4 > ClaimTTL 3; state ∈ {active,draining}
		t.Fatal(err)
	}
	store, ok := repo.(auditgovernance.Store)
	if !ok {
		t.Fatal("repository is not an audit governance store")
	}
	runtime, err := auditgovernance.New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// LIFO: runtime.Close() executes BEFORE repo.Close() — the reverse
	// registration would close the store the runtime still queries.
	t.Cleanup(runtime.Close)
	return runtime, store, dsn
}

// drillRuntimeConfig is the shared drill binding config (one active acme
// binding, maxLag 4s > claimTTL 3s) used by the drill runtime helpers.
func drillRuntimeConfig() config.AuditGovernanceConfig {
	return config.AuditGovernanceConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:1", TokenURL: "http://127.0.0.1:1/token",
		HMACKey: "audit-governance-hmac-key-32-bytes-minimum", Revision: 1,
		HTTPTimeoutSeconds: 1, PollMilliseconds: 10, BatchSize: 10, ClaimTTLSeconds: 3,
		InitialBackoffSeconds: 1, MaxBackoffSeconds: 2, MaxLagSeconds: 4,
		ReconcileBatchSize: 20, DeliveredRetentionSeconds: 3600,
		CleanupIntervalSeconds: 60, CleanupBatchSize: 20,
		Bindings: []config.AuditGovernanceBinding{{
			TenantID: "acme", ClientID: "vault-audit",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_ACME",
			ClientSecret:    "machine-secret", State: "active",
		}},
	}
}

// seedPendingDrillFact inserts one pending audit-governance fact for tenant
// acme through the public store API (the same shape runtime_test.go:415
// seeds; the outbox row's created_at_ns is the backlog-age anchor).
func seedPendingDrillFact(t *testing.T, ctx context.Context, store auditgovernance.Store) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := store.InsertEventWithGovernance(ctx, repository.Event{
		TenantID: "acme", Bucket: "b", Key: "k", Type: repository.EventCreated,
		CreatedAt: now,
	}, repository.AuditGovernanceFact{SourceID: "acme", TenantID: "acme",
		OriginKind: repository.AuditOriginFile, FactKind: "file",
		Action: "file.create", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
}

// backdateDrillFact rewrites created_at_ns on the seeded pending row so the
// backlog age crosses a chosen threshold deterministically — no sleeps. WAL
// permits a second writer on the same file DSN (internal/reconcile
// lifecycle_test.go idiom); the repo serializes only its own pool.
func backdateDrillFact(t *testing.T, dsn string, age time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn) // same driver name as internal/repository
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cut := time.Now().UTC().Add(-age).UnixNano()
	if _, err := db.Exec(
		`UPDATE audit_governance_outbox SET created_at_ns = ?`+
			` WHERE tenant_id = 'acme' AND delivered_at_ns = 0 AND failed_at_ns = 0`, cut); err != nil {
		t.Fatal(err)
	}
}

// serveReadyz runs the real readyzHandler (healthy storage stub so the full
// 2s budget reaches the extra probe) and returns status, body, elapsed.
func serveReadyz(t *testing.T, extra readinessChecker) (int, string, time.Duration) {
	t.Helper()
	h := readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, extra)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec.Code, rec.Body.String(), time.Since(start)
}

// TestReadyzExtraProbeTimeout proves a wedged extra probe yields 503 within
// ~2s: the blocking stub can only return after probeCtx's deadline fires, so
// the response cannot precede it; the ≤ 5s upper bound only proves
// boundedness (same bounds as TestReadyzStorageProbeTimeout, http_test.go).
func TestReadyzExtraProbeTimeout(t *testing.T) {
	code, body, elapsed := serveReadyz(t, &blockingReadyChecker{})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d", code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(body, "runtime dependency unavailable") {
		t.Fatalf("body=%q want it to contain %q", body, "runtime dependency unavailable")
	}
	if elapsed < time.Second {
		t.Fatalf("elapsed=%v: response cannot precede the 2s probe deadline", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed=%v: probe deadline did not bound the blocked Ready", elapsed)
	}
}

// TestReadyzImmediateExtraError pins that the deadline wrap neither delays
// nor swallows non-deadline errors from the extra probe.
func TestReadyzImmediateExtraError(t *testing.T) {
	code, body, elapsed := serveReadyz(t, &errorReadyChecker{})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d", code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(body, "runtime dependency unavailable") {
		t.Fatalf("body=%q want it to contain %q", body, "runtime dependency unavailable")
	}
	if elapsed >= time.Second {
		t.Fatalf("elapsed=%v: immediate extra error must not be delayed by the wrap", elapsed)
	}
}

// TestReadyzHealthyExtra200 pins the healthy-extra path: 200 with the exact
// body, no degraded marker (degradation is gauge/alert-carried, D4).
func TestReadyzHealthyExtra200(t *testing.T) {
	code, body, elapsed := serveReadyz(t, &okReadyChecker{})
	if code != http.StatusOK {
		t.Fatalf("status=%d want %d", code, http.StatusOK)
	}
	if body != `{"ok":true}` {
		t.Fatalf("body=%q want %q", body, `{"ok":true}`)
	}
	if elapsed >= time.Second {
		t.Fatalf("elapsed=%v: healthy extra must answer immediately", elapsed)
	}
}

// TestReadyzBacklogLagDegradesNot503 pins B3-2 (D1) at the HTTP seam: a
// pending backlog backdated 8s (> maxLag 4s, 2× margin) still yields 200 —
// but now WITH the degraded marker body: lag > maxLag is a payload, never a
// 503. The marker is the wedge signal (F11: age 0 on probe timeout) and the
// healthy 200 body stays byte-identical elsewhere (http_test.go).
func TestReadyzBacklogLagDegradesNot503(t *testing.T) {
	ctx := context.Background()
	rt, store, dsn := newReadyzDrillRuntime(t)
	seedPendingDrillFact(t, ctx, store)
	backdateDrillFact(t, dsn, 8*time.Second)

	// Pre-assert the degraded condition really holds before the seam runs.
	age, ok, err := rt.PendingBacklogAge(ctx)
	if err != nil || !ok {
		t.Fatalf("PendingBacklogAge ok=%v err=%v, want pending backlog", ok, err)
	}
	if age <= 4*time.Second {
		t.Fatalf("PendingBacklogAge=%v, want > maxLag (4s)", age)
	}

	code, body, elapsed := serveReadyz(t, runtimeReadiness(nil, rt))
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200 (degraded, never 503)", code)
	}
	const want = `{"ok":true,"degraded":true,"backlog_age_seconds":8}`
	if body != want {
		// Safety net: the backdate→probe path is sub-second (the elapsed
		// assertions below already require it), so 8s+ε truncates to 8; if a
		// slow machine ever crossed a truncation boundary, the parsed age
		// floor still proves the marker carries the real lag.
		if !strings.HasPrefix(body, `{"ok":true,"degraded":true,"backlog_age_seconds":`) {
			t.Fatalf("body=%q want marker body %q", body, want)
		}
		ageStr := strings.TrimSuffix(
			strings.TrimPrefix(body, `{"ok":true,"degraded":true,"backlog_age_seconds":`), "}")
		age, err := strconv.ParseInt(ageStr, 10, 64)
		if err != nil || age < 8 {
			t.Fatalf("body=%q: parsed age=%v err=%v, want floor >= 8", body, age, err)
		}
	}
	if elapsed >= time.Second {
		t.Fatalf("elapsed=%v: healthy store must answer well within the 2s budget", elapsed)
	}
}

// TestReadyzDrainStill503 is the boundary control: a draining binding WITH a
// pending fact must 503 (HasPendingDrainingAuditGovernance). Capture only
// admits active bindings, so seed while active, then rebind to draining —
// the runtime_test.go:415-462 idiom. Without this negative control a bug
// that skipped `extra` entirely would pass TestReadyzBacklogLagDegradesNot503
// vacuously.
func TestReadyzDrainStill503(t *testing.T) {
	ctx := context.Background()
	rt, store, _ := newReadyzDrillRuntime(t)
	seedPendingDrillFact(t, ctx, store)
	if err := store.ApplyAuditGovernanceBindings(ctx, 2, "acme-v2",
		[]repository.AuditGovernanceBindingState{
			{TenantID: "acme", State: repository.AuditGovernanceBindingDraining},
		}); err != nil {
		t.Fatal(err)
	}

	code, body, elapsed := serveReadyz(t, runtimeReadiness(nil, rt))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d (drain in progress)", code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(body, "runtime dependency unavailable") {
		t.Fatalf("body=%q want it to contain %q", body, "runtime dependency unavailable")
	}
	if elapsed >= time.Second {
		t.Fatalf("elapsed=%v: drain lookup is a single fast query", elapsed)
	}
}

// TestReadyzDeadLetteredBacklog200AndGaugeZero pins the terminal shapes at
// the seam: phase 0 an EMPTY store (no rows at all), phase 1 a fully
// dead-lettered backlog (failed_at_ns != 0, delivered 0 — never re-claimed)
// — both report ok=false, gauge 0 and 200; phase 2 is the live-row control:
// a fresh pending fact backdated a deterministic 2s (below maxLag 4s →
// still 200; above the int64(age.Seconds()) truncation floor → gauge ≥ 2,
// never the constant-zero the terminal phases would mask).
func TestReadyzDeadLetteredBacklog200AndGaugeZero(t *testing.T) {
	ctx := context.Background()
	rt, store, dsn := newReadyzDrillRuntime(t)
	gauge := auditGovernanceBacklogAgeGaugeFn(rt)
	degradedGauge := auditGovernanceDegradedGaugeFn(rt)

	// Phase 0 — empty store: terminal pin, not just the fresh-store case.
	if _, ok, err := rt.PendingBacklogAge(ctx); err != nil || ok {
		t.Fatalf("empty store: PendingBacklogAge ok=%v err=%v, want ok=false", ok, err)
	}
	if got := gauge(ctx); got != 0 {
		t.Fatalf("empty store: gauge=%d want 0", got)
	}
	if code, body, elapsed := serveReadyz(t, runtimeReadiness(nil, rt)); code != http.StatusOK ||
		body != `{"ok":true}` || elapsed >= time.Second {
		t.Fatalf("empty store: code=%d body=%q elapsed=%v, want 200 %q <1s",
			code, body, elapsed, `{"ok":true}`)
	}
	if got := degradedGauge(ctx); got != 0 {
		t.Fatalf("empty store: degradedGauge=%d want 0", got)
	}

	// Phase 1 — dead-lettered backlog via the public lease-fenced API: claim
	// (revision 1, the binding New applied) then Fail lands the row terminal.
	seedPendingDrillFact(t, ctx, store)
	facts, err := store.ClaimAuditGovernance(ctx, "acme", "tok", 1, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("claimed %d facts, want 1", len(facts))
	}
	if err := store.FailAuditGovernance(ctx, facts[0].ID, "acme", "tok", "dead"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := rt.PendingBacklogAge(ctx); err != nil || ok {
		t.Fatalf("dead-lettered: PendingBacklogAge ok=%v err=%v, want ok=false", ok, err)
	}
	if got := gauge(ctx); got != 0 {
		t.Fatalf("dead-lettered: gauge=%d want 0", got)
	}
	if code, body, elapsed := serveReadyz(t, runtimeReadiness(nil, rt)); code != http.StatusOK ||
		body != `{"ok":true}` || elapsed >= time.Second {
		t.Fatalf("dead-lettered: code=%d body=%q elapsed=%v, want 200 %q <1s",
			code, body, elapsed, `{"ok":true}`)
	}
	if got := degradedGauge(ctx); got != 0 {
		t.Fatalf("dead-lettered: degradedGauge=%d want 0 (dead rows are not degraded)", got)
	}

	// Phase 2 — live-row control: the callback reports real ages, not the
	// constant zero the terminal phases would mask. 2s backdate is
	// deterministic: age = 2s + process-elapsed ≥ 2s at read, so the
	// truncating int64(age.Seconds()) is ≥ 2 regardless of machine speed.
	// D3: the callback reads the run-loop cache, so the drill primes it with
	// one live probe first (the relay never starts here — a zero-initialized
	// cache would otherwise report the constant zero).
	seedPendingDrillFact(t, ctx, store)
	backdateDrillFact(t, dsn, 2*time.Second)
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("priming probe: %v", err)
	}
	if _, ok, err := rt.PendingBacklogAge(ctx); err != nil || !ok {
		t.Fatalf("live row: PendingBacklogAge ok=%v err=%v, want ok=true", ok, err)
	}
	if got := gauge(ctx); got <= 0 {
		t.Fatalf("live row: gauge=%d want > 0 (backdated 2s > truncation floor)", got)
	}
	if code, body, elapsed := serveReadyz(t, runtimeReadiness(nil, rt)); code != http.StatusOK ||
		body != `{"ok":true}` || elapsed >= time.Second {
		t.Fatalf("live row: code=%d body=%q elapsed=%v, want 200 %q <1s",
			code, body, elapsed, `{"ok":true}`)
	}
	if got := degradedGauge(ctx); got != 0 {
		t.Fatalf("live row: degradedGauge=%d want 0 (age 2s < maxLag 4s)", got)
	}
}

// TestAlertsYMLAuditGovernanceExprParity pins the B3-2 degraded alert to the
// gauge the seam reports: expr name == exported gauge name (dots→underscores,
// telemetry/metrics.go), threshold == the SHIPPED AUDIT_GOVERNANCE_MAX_LAG_SECONDS
// default × 0.5 — DERIVED via config.Load() (the same loader main.go uses)
// with the env neutralized, so an operator override can never shift what the
// static alerts.yml is compared against — severity warning, the
// "/readyz stays 200" contract in the rule description, and the F11/F16
// OR-arm (v3 amendment): `for: 10m`, `OR audit_governance_degraded == 1`,
// and exactly two `expr: audit_governance_` exprs file-wide (this rule and
// the AuditGovernanceEnabledUnbound gate companion — no third
// audit_governance_* name in any expr). The default side of the ×0.5
// arithmetic is pinned at its owner in internal/config
// (TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold); either side
// drifting alone now fails CI (the old hand-kept alertLagThresholdSeconds
// literal is gone — no second constant to drift). Stdlib-only
// (os.ReadFile + strings + strconv, I6 — no YAML dependency promotion).
func TestAlertsYMLAuditGovernanceExprParity(t *testing.T) {
	t.Setenv("AUDIT_GOVERNANCE_ENABLED", "false")    // skip the bindings-file requirement
	t.Setenv("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", "") // empty → getEnvInt falls back to the shipped default
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	wantExpr := "expr: audit_governance_backlog_age_seconds > " +
		strconv.Itoa(cfg.AuditGovernance.BacklogAlertThresholdSeconds())

	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "prometheus", "alerts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// The OR arm is the v3 amendment's whole point: a regression dropping
	// it (the wedge would go alert-silent again, F11/F16) must fail CI.
	// Exactly two audit_governance exprs exist: this rule and the
	// AuditGovernanceEnabledUnbound drain-mode gate companion.
	if n := strings.Count(string(data), "expr: audit_governance_"); n != 2 {
		t.Fatalf("alerts.yml has %d audit_governance exprs, want exactly 2", n)
	}
	marker := "alert: AuditGovernanceBacklogDegraded"
	idx := strings.Index(string(data), marker)
	if idx < 0 {
		t.Fatalf("alerts.yml missing rule %q", marker)
	}
	block := string(data)[idx:] // block-scope to EOF; the rule is the file's last
	for _, want := range []string{
		wantExpr,
		"OR audit_governance_degraded == 1",
		"for: 10m",
		"severity: warning",
		"/readyz stays 200",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("AuditGovernanceBacklogDegraded block missing %q", want)
		}
	}
}

// hangingAuditStore wraps a real audit-governance store and makes only the
// two readiness probe methods hang on the caller context (the wedged-store
// shape); every other method delegates (New() applies the acme binding
// through the wrapper before the drill wedges the probes).
type hangingAuditStore struct {
	auditgovernance.Store
}

func (s *hangingAuditStore) HasPendingDrainingAuditGovernance(ctx context.Context) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (s *hangingAuditStore) OldestPendingAuditGovernance(ctx context.Context) (time.Time, bool, error) {
	<-ctx.Done()
	return time.Time{}, false, ctx.Err()
}

// TestReadyzAuditGovernanceDegradedDrill is the F11 end-to-end pin at the
// seam: a wedged relay store (probe hang) yields 200 with the degraded
// marker and age 0 (age unknown, REQ-3) — never 503 — within the 2s probe
// budget; and the wedge is visible in /metrics while the age gauge reads 0
// (the degraded-flag gauge is the F11/F16 alert arm).
func TestReadyzAuditGovernanceDegradedDrill(t *testing.T) {
	ctx := context.Background()
	rt := newWedgedReadyzDrillRuntime(t)

	code, body, elapsed := serveReadyz(t, runtimeReadiness(nil, rt))
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200 (wedged store degrades, never 503)", code)
	}
	if !strings.Contains(body, `"degraded":true`) {
		t.Fatalf("body=%q want degraded marker", body)
	}
	if elapsed < time.Second {
		t.Fatalf("elapsed=%v: response cannot precede the 2s probe deadline", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed=%v: probe deadline did not bound the wedged probes", elapsed)
	}
	// The wedge is /metrics-visible with age 0: ageGauge=0 ∧ degradedGauge=1.
	if got := auditGovernanceBacklogAgeGaugeFn(rt)(ctx); got != 0 {
		t.Fatalf("ageGauge=%d want 0 (age unknown on probe timeout)", got)
	}
	if got := auditGovernanceDegradedGaugeFn(rt)(ctx); got != 1 {
		t.Fatalf("degradedGauge=%d want 1 (wedge is the degraded arm)", got)
	}
}

// newWedgedReadyzDrillRuntime is newReadyzDrillRuntime's wedge sibling: the
// store is wrapped so both readiness probes hang; the relay never starts, so
// Runtime.Close is non-blocking; LIFO teardown (repo first, runtime second).
func newWedgedReadyzDrillRuntime(t *testing.T) *auditgovernance.Runtime {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "wedge.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := drillRuntimeConfig()
	store, ok := repo.(auditgovernance.Store)
	if !ok {
		t.Fatal("repository is not an audit governance store")
	}
	runtime, err := auditgovernance.New(cfg, &hangingAuditStore{Store: store},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime
}

// stripGoCommentsAndStrings removes // line comments and "…"/`…` string
// literals (with escape handling) so the scan sees only executable tokens.
func stripGoCommentsAndStrings(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '"':
			for i++; i < len(src); i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == '"' {
					break
				}
			}
		case '`':
			for i++; i < len(src) && src[i] != '`'; i++ {
			}
		case '/':
			if i+1 < len(src) && src[i+1] == '/' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
				b.WriteByte('\n')
				continue
			}
			b.WriteByte(src[i])
		default:
			b.WriteByte(src[i])
		}
	}
	return b.String()
}

// TestNoExecutable450LiteralOutsideAlertsYml pins acceptance AC-2: the only
// executable 450 threshold literal in the Go tree is the alerts.yml expr
// (pinned separately by TestAlertsYMLAuditGovernanceExprParity). The regex
// lives in this test's own raw string and is stripped before matching, so the
// scan cannot self-hit. Comments and string literals are stripped — comment
// drift stays allowed; only executable tokens are pinned (FM5).
func TestNoExecutable450LiteralOutsideAlertsYml(t *testing.T) {
	pat := regexp.MustCompile(`\b450\b`)
	// `go test` runs with cwd = the package dir (cmd/server), so the roots
	// must be anchored to the repo root via the same `../..` the parity
	// test uses for deploy/prometheus/alerts.yml — a relative "cmd" walk
	// would traverse cmd/server/cmd and silently see nothing.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, root := range []string{"cmd", "internal", "sdk/go"} { // mcp lives at internal/mcp
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err // mid-walk errors must be loud, never silent skips
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(pat.FindAllStringIndex(stripGoCommentsAndStrings(string(src)), -1)); n > 0 {
				rel, _ := filepath.Rel(repoRoot, path)
				hits = append(hits, fmt.Sprintf("%s: %d", rel, n))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err) // loud: a relocated test walking nothing must fail, not pass
		}
	}
	if len(hits) > 0 {
		t.Fatalf("executable 450 literal(s) outside alerts.yml: %v", hits)
	}
}
