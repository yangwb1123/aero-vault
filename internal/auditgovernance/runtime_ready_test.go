package auditgovernance

// runtime_ready_test.go pins the D1 read-path half of B3-2: Ready()'s two
// store probes are bounded by storeProbeTimeout, probe timeout/cancel records
// a degraded sentinel (nil, never 503), genuine store errors stay fail-closed,
// terminal (dead-lettered) rows never block readiness, and the run loop keeps
// the degraded cache fresh independent of /readyz traffic. Determinism: the
// hanging probes block on the ctx deadline (response cannot precede the 2s
// bound), WAL second-writer backdating replaces sleeps, and the concurrent
// pair-discipline test is CI-enforced under -race via Makefile test-race-meta.

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// scriptedStore wraps a real Store and injects readiness-probe behavior:
// mode healthy delegates both probes; mode lag makes OldestPendingAuditGovernance
// return a 2h-backdated row (lag > maxLag); mode hang blocks both probes until
// the caller context is done (the wedged-store shape); backlogHang (overlay,
// set via setBacklogHang) blocks only OldestPendingAuditGovernance — the
// per-probe wedge shape (RG-1); drainErr/backlogErr inject immediate
// non-context errors per probe (the fail-closed shape). All other methods
// delegate unconditionally — the run loop keeps working through the scripted
// probes (F17).
type scriptedStore struct {
	store       Store
	mu          sync.Mutex
	hang        bool
	backlogHang bool
	lag         bool
	drainErr    error
	backlogErr  error
	// panicBacklog (overlay, set via setPanicBacklog) makes
	// OldestPendingAuditGovernance panic — the REQ-2 proof that the gauge
	// callback performs zero store I/O per scrape: a regression that re-adds
	// a store query to the callback panics inside the OTel callback (test
	// failure = loud). Arm only after the final Ready() of a phase; setMode
	// clears it (same total-reset discipline as backlogHang).
	panicBacklog bool
}

// setMode is the total-reset primitive: it resets all probe state including
// backlogHang, so a stale per-probe wedge can never survive a mode transition.
func (s *scriptedStore) setMode(hang, lag bool, drainErr, backlogErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hang, s.lag = hang, lag
	s.backlogHang = false
	s.panicBacklog = false
	s.drainErr, s.backlogErr = drainErr, backlogErr
}

// setPanicBacklog overlays the backlog-probe panic; setMode clears it. Apply
// it only after the final setMode + Ready() of a scenario — while armed, any
// probe (Ready()/Start()) panics by design.
func (s *scriptedStore) setPanicBacklog(p bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panicBacklog = p
}

// setBacklogHang overlays the backlog-probe hang; setMode clears it. Apply it
// only after the final setMode of a scenario.
func (s *scriptedStore) setBacklogHang(h bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backlogHang = h
}

func (s *scriptedStore) HasPendingDrainingAuditGovernance(ctx context.Context) (bool, error) {
	s.mu.Lock()
	hang, drainErr := s.hang, s.drainErr
	s.mu.Unlock()
	if hang {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if drainErr != nil {
		return false, drainErr
	}
	return s.store.HasPendingDrainingAuditGovernance(ctx)
}

func (s *scriptedStore) OldestPendingAuditGovernance(ctx context.Context) (time.Time, bool, error) {
	s.mu.Lock()
	hang, backlogHang, lag, backlogErr, panicBacklog := s.hang, s.backlogHang, s.lag, s.backlogErr, s.panicBacklog
	s.mu.Unlock()
	if panicBacklog {
		panic("store query from gauge callback") // REQ-2: must be unreachable from the gauge path
	}
	if hang || backlogHang {
		<-ctx.Done()
		return time.Time{}, false, ctx.Err()
	}
	if backlogErr != nil {
		return time.Time{}, false, backlogErr
	}
	if lag {
		return time.Now().Add(-2 * time.Hour), true, nil
	}
	return s.store.OldestPendingAuditGovernance(ctx)
}

func (s *scriptedStore) ApplyAuditGovernanceBindings(ctx context.Context, revision uint64, digest string, bindings []repository.AuditGovernanceBindingState) error {
	return s.store.ApplyAuditGovernanceBindings(ctx, revision, digest, bindings)
}

func (s *scriptedStore) AuditGovernanceCanDisable(ctx context.Context) (bool, error) {
	return s.store.AuditGovernanceCanDisable(ctx)
}

func (s *scriptedStore) RecordAuditWithGovernance(ctx context.Context, entry repository.AuditEntry, fact repository.AuditGovernanceFact) error {
	return s.store.RecordAuditWithGovernance(ctx, entry, fact)
}

func (s *scriptedStore) InsertEventWithGovernance(ctx context.Context, event repository.Event, fact repository.AuditGovernanceFact) (int64, error) {
	return s.store.InsertEventWithGovernance(ctx, event, fact)
}

func (s *scriptedStore) ListAuditGovernanceGaps(ctx context.Context, tenant string, limit int) ([]repository.AuditGovernanceGap, error) {
	return s.store.ListAuditGovernanceGaps(ctx, tenant, limit)
}

func (s *scriptedStore) EnqueueAuditGovernance(ctx context.Context, fact repository.AuditGovernanceFact) (bool, error) {
	return s.store.EnqueueAuditGovernance(ctx, fact)
}

func (s *scriptedStore) ClaimAuditGovernance(ctx context.Context, tenant, token string, revision uint64, limit int, ttl time.Duration) ([]repository.AuditGovernanceFact, error) {
	return s.store.ClaimAuditGovernance(ctx, tenant, token, revision, limit, ttl)
}

func (s *scriptedStore) CompleteAuditGovernance(ctx context.Context, id, tenant, token string) error {
	return s.store.CompleteAuditGovernance(ctx, id, tenant, token)
}

func (s *scriptedStore) RetryAuditGovernance(ctx context.Context, id, tenant, token, cause string, nextAttempt time.Time) error {
	return s.store.RetryAuditGovernance(ctx, id, tenant, token, cause, nextAttempt)
}

func (s *scriptedStore) FailAuditGovernance(ctx context.Context, id, tenant, token, cause string) error {
	return s.store.FailAuditGovernance(ctx, id, tenant, token, cause)
}

func (s *scriptedStore) RejectAuditGovernance(ctx context.Context, id, tenant, token, cause string) error {
	return s.store.RejectAuditGovernance(ctx, id, tenant, token, cause)
}

func (s *scriptedStore) CleanupDeliveredAuditGovernance(ctx context.Context, before time.Time, limit int) (int64, error) {
	return s.store.CleanupDeliveredAuditGovernance(ctx, before, limit)
}

func (s *scriptedStore) CleanupFailedAuditGovernance(ctx context.Context, before time.Time, limit int) (int64, error) {
	return s.store.CleanupFailedAuditGovernance(ctx, before, limit)
}

// newReadyRuntime builds a Runtime over a real SQLite store wrapped in a
// scriptedStore (probe injection), with the harness config (maxLag 4s, poll
// 10ms, loopback publisher so New makes no network calls).
func newReadyRuntime(t *testing.T) (*Runtime, *scriptedStore, string) {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "ready_probe.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, ok := repo.(Store)
	if !ok {
		t.Fatal("repo is not an audit governance store")
	}
	scripted := &scriptedStore{store: store}
	runtime, err := New(runtimeConfig("http://127.0.0.1:1"), scripted,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime, scripted, dsn
}

// pollUntilReady spins on cond until it holds or the deadline passes (no
// sleeps beyond the poll interval; all call sites have ≥ 2× timing slack).
// (Distinct from relay_terminal_test.go's pollUntil — different signature.)
func pollUntilReady(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

// TestRuntimeReadyDegradedSentinel (T1b) pins the D1 read-path half: a
// wedged relay store (probe hang) makes Ready() return nil with the degraded
// sentinel and age 0 (age unknown, REQ-3) — bounded by storeProbeTimeout
// (elapsed ∈ [1s, 5s]: the blocking probe cannot precede the 2s deadline).
// The backlog-probe-only subtest pins the acceptance's literal shape (RG-1):
// only OldestPendingAuditGovernance times out (drain probe healthy). The fork
// at runtime.go:268-272 must produce the SAME output as the both-wedged drain
// fork — nil, degraded, age 0 — so /readyz renders one marker.
func TestRuntimeReadyDegradedSentinel(t *testing.T) {
	rt, scripted, _ := newReadyRuntime(t)
	scripted.setMode(true, false, nil, nil)

	start := time.Now()
	err := rt.Ready(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Ready=%v, want nil (wedged store degrades, never 503)", err)
	}
	if elapsed < time.Second {
		t.Fatalf("elapsed=%v: response cannot precede the 2s probe deadline", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed=%v: probe deadline did not bound the wedged probes", elapsed)
	}
	if !rt.Degraded() {
		t.Fatal("Degraded()=false, want true after a probe timeout")
	}
	if got := rt.BacklogAge(); got != 0 {
		t.Fatalf("BacklogAge()=%v want 0 (age unknown on probe timeout)", got)
	}

	t.Run("backlog-probe-only", func(t *testing.T) {
		rt, scripted, _ := newReadyRuntime(t)
		// Order composes with the reset semantics: setMode is the total-reset
		// primitive (clears backlogHang), so the overlay comes last — no
		// pre-setMode reset call needed.
		scripted.setMode(false, false, nil, nil) // drain probe healthy
		scripted.setBacklogHang(true)            // only OldestPending wedges
		start := time.Now()
		err := rt.Ready(context.Background())
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Ready=%v, want nil (backlog-probe timeout degrades, never 503)", err)
		}
		if elapsed < time.Second {
			t.Fatalf("elapsed=%v: response cannot precede the 2s probe deadline", elapsed)
		}
		if elapsed > 5*time.Second {
			t.Fatalf("elapsed=%v: probe deadline did not bound the wedged backlog probe", elapsed)
		}
		if !rt.Degraded() {
			t.Fatal("Degraded()=false, want true after a backlog-probe timeout")
		}
		if got := rt.BacklogAge(); got != 0 {
			t.Fatalf("BacklogAge()=%v want 0 (age unknown on probe timeout)", got)
		}
	})
}

// TestRuntimeReadyFailClosedOnGenuineStoreError (T1c) pins the preserved
// fail-closed branches: a genuine non-context store error is a hard readiness
// failure (never degraded) — c1 on the drain probe, c2 on the backlog probe.
// (c3, F18 pin) a pre-canceled ctx records degraded and returns nil — the
// context.Canceled side of the isProbeCtxError fork.
func TestRuntimeReadyFailClosedOnGenuineStoreError(t *testing.T) {
	t.Run("c1-drain-error", func(t *testing.T) {
		rt, scripted, _ := newReadyRuntime(t)
		scripted.setMode(false, false, errors.New("injected drain failure"), nil)
		err := rt.Ready(context.Background())
		if err == nil || err.Error() != "audit governance drain lookup failed" {
			t.Fatalf("Ready=%v, want %q", err, "audit governance drain lookup failed")
		}
		if rt.Degraded() {
			t.Fatal("Degraded()=true, want false (genuine error is fail-closed, not degraded)")
		}
	})
	t.Run("c2-backlog-error", func(t *testing.T) {
		rt, scripted, _ := newReadyRuntime(t)
		scripted.setMode(false, false, nil, errors.New("injected backlog failure"))
		err := rt.Ready(context.Background())
		if err == nil || err.Error() != "audit governance backlog lookup failed" {
			t.Fatalf("Ready=%v, want %q", err, "audit governance backlog lookup failed")
		}
		if rt.Degraded() {
			t.Fatal("Degraded()=true, want false (genuine error is fail-closed, not degraded)")
		}
	})
	t.Run("c3-pre-canceled-ctx", func(t *testing.T) {
		rt, scripted, _ := newReadyRuntime(t)
		scripted.setMode(true, false, nil, nil) // hang — but the ctx is already canceled
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		err := rt.Ready(canceled)
		if err != nil {
			t.Fatalf("Ready=%v, want nil (caller cancel is a probe-ctx error)", err)
		}
		if elapsed := time.Since(start); elapsed >= time.Second {
			t.Fatalf("elapsed=%v: pre-canceled ctx must return immediately, not wait out the probe", elapsed)
		}
		if !rt.Degraded() {
			t.Fatal("Degraded()=false, want true (context.Canceled records degraded)")
		}
		if got := rt.BacklogAge(); got != 0 {
			t.Fatalf("BacklogAge()=%v want 0 (age unknown)", got)
		}
	})
}

// TestRuntimeBacklogAgeZeroWhenAllTerminal (T5) pins the terminal-row
// exclusion at runtime level: a dead-lettered backlog (Claim + Fail via the
// public lease-fenced API) reports ok=false, gauge 0, Ready nil, not degraded.
func TestRuntimeBacklogAgeZeroWhenAllTerminal(t *testing.T) {
	ctx := context.Background()
	rt, scripted, _ := newReadyRuntime(t)
	scripted.setMode(false, false, nil, nil) // healthy probes

	now := time.Now().UTC()
	if _, err := rt.store.InsertEventWithGovernance(ctx, repository.Event{
		TenantID: "acme", Bucket: "b", Key: "k", Type: repository.EventCreated,
		CreatedAt: now,
	}, repository.AuditGovernanceFact{SourceID: "acme", TenantID: "acme",
		OriginKind: repository.AuditOriginFile, FactKind: "file",
		Action: "file.create", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	facts, err := rt.store.ClaimAuditGovernance(ctx, "acme", "tok", 1, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("claimed %d facts, want 1", len(facts))
	}
	if err := rt.store.FailAuditGovernance(ctx, facts[0].ID, "acme", "tok", "conflict:true"); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := rt.store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("OldestPending ok=%v err=%v, want ok=false (terminal rows excluded)", ok, err)
	}
	if _, ok, err := rt.PendingBacklogAge(ctx); err != nil || ok {
		t.Fatalf("PendingBacklogAge ok=%v err=%v, want ok=false", ok, err)
	}
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready=%v, want nil (dead-lettered backlog never blocks readiness)", err)
	}
	if rt.Degraded() {
		t.Fatal("Degraded()=true, want false")
	}
	if got := rt.BacklogAge(); got != 0 {
		t.Fatalf("cache BacklogAge()=%v want 0", got)
	}
}

// TestRuntimeDeadRowsExcludedWhilePendingRowsVisible (REQ-1) pins the T-3
// predicate-preservation half at runtime level: a MIXED backlog (one row
// dead-lettered via Claim + Fail, one still pending) reports the survivor —
// OldestPending ok=true, PendingBacklogAge ok=true age>0, cache > 0, Ready
// nil, not degraded. The deterministic age phase then WAL-backdates the
// surviving row only (−16s > maxLag) and proves the cache degrades WHILE the
// failed row is still stored — a terminal row contributes 0 to the gauge
// regardless of its created_at_ns. Determinism: the seeds are two distinct
// event rows (distinct origin ids, hence distinct deterministic fact IDs —
// an G4 dedupe trap would need an identically-framed ID, impossible across
// event rows), terminalization goes through the lease-fenced public API, and
// the backdate helper matches only still-pending rows (exactly one survives).
func TestRuntimeDeadRowsExcludedWhilePendingRowsVisible(t *testing.T) {
	ctx := context.Background()
	rt, scripted, dsn := newReadyRuntime(t)
	scripted.setMode(false, false, nil, nil) // healthy probes

	now := time.Now().UTC()
	// Two distinct seeds: distinct FactKind AND Action so the deterministic
	// IDs (and the runtime's view) are separable (G4).
	for _, s := range []struct {
		kind, action string
	}{
		{"file", "file.create"},
		{"security", "key.add"},
	} {
		if _, err := rt.store.InsertEventWithGovernance(ctx, repository.Event{
			TenantID: "acme", Bucket: "b", Key: "k-" + s.action, Type: repository.EventCreated,
			CreatedAt: now,
		}, repository.AuditGovernanceFact{SourceID: "acme", TenantID: "acme",
			OriginKind: repository.AuditOriginFile, FactKind: s.kind,
			Action: s.action, OccurredAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	// Baseline: both rows pending (count-2 — the F7 dedupe guard).
	if n := countPendingOutboxRows(t, dsn); n != 2 {
		t.Fatalf("pending rows=%d want 2 before terminalize (seed dedupe?)", n)
	}

	// Terminalize the oldest via the public lease-fenced API (fencing
	// contract pinned at repo level, audit_governance_test.go).
	facts, err := rt.store.ClaimAuditGovernance(ctx, "acme", "tok", 1, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("claimed %d facts, want 2 (both seeds must survive)", len(facts))
	}
	if err := rt.store.FailAuditGovernance(ctx, facts[0].ID, "acme", "tok", "conflict:true"); err != nil {
		t.Fatal(err)
	}
	if facts[0].ID == facts[1].ID {
		t.Fatalf("seed IDs collided (%q) — deterministic dedupe merged the seeds", facts[0].ID)
	}

	// Predicate preservation: the survivor is visible, the failed row is not.
	if _, ok, err := rt.store.OldestPendingAuditGovernance(ctx); err != nil || !ok {
		t.Fatalf("OldestPending ok=%v err=%v, want ok=true (surviving row visible)", ok, err)
	}
	if age, ok, err := rt.PendingBacklogAge(ctx); err != nil || !ok || age <= 0 {
		t.Fatalf("PendingBacklogAge ok=%v age=%v err=%v, want ok=true age>0", ok, age, err)
	}
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready=%v, want nil (young survivor, no lag)", err)
	}
	if rt.Degraded() {
		t.Fatal("Degraded()=true, want false (survivor age < maxLag)")
	}
	if got := rt.BacklogAge(); got <= 0 {
		t.Fatalf("cache BacklogAge()=%v want > 0 (gauge source non-zero)", got)
	}
	// The exact reverse of the all-terminal pin: one terminal row must NOT
	// zero the gauge while a pending row survives.
	if got := countTerminalOutboxRows(t, dsn); got != 1 {
		t.Fatalf("terminal rows=%d want 1 (failed row still stored)", got)
	}

	// Deterministic age phase (G3): backdate the surviving row only −16s (>)
	// and prove the lag is computed from the pending row only — the failed
	// row's created_at_ns is irrelevant (predicate excludes it), which is
	// precisely the assertion.
	backdatePendingFact(t, dsn, 16*time.Second)
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready=%v, want nil (backdated survivor degrades, never fails)", err)
	}
	if !rt.Degraded() {
		t.Fatal("Degraded()=false, want true after backdating the surviving row")
	}
	if got := rt.BacklogAge(); got <= 4*time.Second {
		t.Fatalf("cache BacklogAge()=%v want > maxLag (4s) computed from survivor only", got)
	}
}

// countPendingOutboxRows counts pending (non-terminal) audit-governance rows
// of tenant acme directly via SQL — the F7 dedupe guard for REQ-1's two
// seeds. The run loop is NOT started in REQ-1's harness, so the second-writer
// connection needs no busy-timeout pragma (plain DSN, backdatePendingFact
// idiom).
func countPendingOutboxRows(t *testing.T, dsn string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM audit_governance_outbox WHERE tenant_id='acme'` +
			` AND delivered_at_ns=0 AND failed_at_ns=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// countTerminalOutboxRows counts failed (terminal) audit-governance rows of
// tenant acme directly — the "failed row still present" half of the REQ-1
// backdate assertion.
func countTerminalOutboxRows(t *testing.T, dsn string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM audit_governance_outbox WHERE tenant_id='acme'` +
			` AND failed_at_ns!=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// backdatePendingFact rewrites created_at_ns on the seeded pending row so the
// backlog age crosses a chosen threshold deterministically — no sleeps. WAL
// permits a second writer on the same file DSN (readyz_drill_test.go idiom).
func backdatePendingFact(t *testing.T, dsn string, age time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
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

// restorePendingFactAge is the inverse of backdatePendingFact: it rewrites
// created_at_ns on the seeded pending row back to now so the backlog age drops
// below maxLag while the row STAYS pending — the deterministic control for the
// run-loop cache fall (see TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls).
// The run loop is live at this point, so the second-writer connection pins a
// busy timeout: the relay's claim/retry writes are brief and only occur on
// available_at boundaries, so contention at most waits out a single statement.
func restorePendingFactAge(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().UnixNano()
	if _, err := db.Exec(
		`UPDATE audit_governance_outbox SET created_at_ns = ?`+
			` WHERE tenant_id = 'acme' AND delivered_at_ns = 0 AND failed_at_ns = 0`, now); err != nil {
		t.Fatal(err)
	}
}

// TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls (T6) proves the run-loop
// feed (G3): with zero Ready() calls, the loop still probes once per poll
// cycle and the degraded cache rises on a backdated backlog (16s > maxLag 4s
// — 12s absolute slack before the age decays to maxLag) and falls on health.
// The fall is driven by restorePendingFactAge — a test-controlled reset of
// created_at_ns to now — so the flip is the healthy probe recording a pending
// backlog at age ≤ maxLag (the asserted G3 semantics), and happens within one
// poll cycle (10ms) of the restore. This is deliberately NOT left to the
// relay's cumulative-window terminal fail: that path empties the backlog (a
// different, unasserted semantic) and its timing — first_attempt + maxBackoff
// backoff schedule, t₀+[2.25s, 3.25s] with the harness jitter — races phase
// 2's 3s deadline with zero slack (~46% of the jitter space fails).
func TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls(t *testing.T) {
	ctx := context.Background()
	rt, scripted, dsn := newReadyRuntime(t)
	scripted.setMode(false, false, nil, nil)
	if _, err := rt.store.InsertEventWithGovernance(ctx, repository.Event{
		TenantID: "acme", Bucket: "b", Key: "k", Type: repository.EventCreated,
		CreatedAt: time.Now().UTC(),
	}, repository.AuditGovernanceFact{SourceID: "acme", TenantID: "acme",
		OriginKind: repository.AuditOriginFile, FactKind: "file",
		Action: "file.create", OccurredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	backdatePendingFact(t, dsn, 16*time.Second)

	rt.Start(ctx)
	pollUntilReady(t, 3*time.Second, func() bool { return rt.Degraded() })
	if got := rt.BacklogAge(); got <= 4*time.Second {
		t.Fatalf("cache BacklogAge()=%v, want > maxLag (4s)", got)
	}
	// Run-loop self-correction: the loop never stops on probe outcomes, so a
	// healthy store restores the cache without any Ready() call. The scripted
	// store has been healthy all along (default mode — the old setMode call
	// here was a no-op); what must change for the cache to fall is the backlog
	// age itself. The row STAYS pending: created_at_ns is only ever written by
	// this restore (claims/retries touch attempts, lease, available_at,
	// first_attempt — never created_at), the claim fence and the cumulative
	// window decision (first_attempt + maxBackoff) are untouched, so the flip
	// is exactly the healthy-probe-with-pending-backlog semantics.
	restorePendingFactAge(t, dsn)
	pollUntilReady(t, 3*time.Second, func() bool { return !rt.Degraded() })
	if got := rt.BacklogAge(); got > 4*time.Second {
		t.Fatalf("cache BacklogAge()=%v, want ≤ maxLag (4s) with a pending backlog", got)
	}
	// Prove the flip came from the healthy probe, not from the cumulative-
	// window terminal path: the backlog is still pending here. The earliest
	// terminalization is t₀+delay₁+delay₂ ≥ t₀+2.25s after Start; the probe
	// flips within one poll cycle (~10ms) of the restore, so the assertion has
	// ≥ 2.2s of slack — and in the pathological case it fails loudly rather
	// than asserting the wrong semantic.
	if _, ok, err := rt.store.OldestPendingAuditGovernance(ctx); err != nil || !ok {
		t.Fatalf("backlog not pending at flip time: ok=%v err=%v (flip must be the healthy-probe path)", ok, err)
	}
	rt.Close()
}

// TestRuntimeRunLoopSurvivesWedgedStore (F17 pin) drives the run loop through
// a wedge: a hanging store flips the cache degraded within the deadline, and
// restoring the healthy store recovers it — the loop keeps cycling through
// wedged probes and never stops.
func TestRuntimeRunLoopSurvivesWedgedStore(t *testing.T) {
	rt, scripted, _ := newReadyRuntime(t)
	scripted.setMode(true, false, nil, nil) // wedge BEFORE Start: iteration 1 hangs

	rt.Start(context.Background())
	pollUntilReady(t, 4*time.Second, func() bool { return rt.Degraded() })

	scripted.setMode(false, false, nil, nil)
	pollUntilReady(t, 3*time.Second, func() bool { return !rt.Degraded() })
	rt.Close()
}

// TestRuntimeDegradedCacheConcurrentAccess (T7) drives Ready() (writers) and
// Degraded()/BacklogAge() (readers) concurrently across scripted healthy→lag→
// hang→healthy transitions and asserts only valid (degraded, age) pairs are
// observable: degraded ⇒ age==0 (unknown/timeout) or age>maxLag; healthy ⇒
// age≤maxLag. The pair discipline is a single-lock write (H0); the
// synchronization is provable only under -race — CI-enforced by Makefile
// test-race-meta (H3).
func TestRuntimeDegradedCacheConcurrentAccess(t *testing.T) {
	rt, scripted, _ := newReadyRuntime(t)
	scripted.setMode(false, false, nil, nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = rt.Ready(context.Background())
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Sample the (degraded, age) pair under ONE RLock acquisition:
			// two sequential public getter calls could straddle a recordDegraded
			// write (each getter is individually consistent, but the pair as
			// stored is what the single-lock discipline guarantees).
			rt.degradedMu.RLock()
			degraded := rt.degraded
			age := rt.backlogAge
			rt.degradedMu.RUnlock()
			if degraded && age != 0 && age <= 4*time.Second {
				t.Errorf("invalid pair: degraded=true age=%v (want 0 or > maxLag 4s)", age)
			}
			if !degraded && age > 4*time.Second {
				t.Errorf("invalid pair: degraded=false age=%v (want ≤ maxLag 4s)", age)
			}
		}
	}()

	scripted.setMode(false, false, nil, nil) // healthy: (false, 0)
	time.Sleep(100 * time.Millisecond)
	scripted.setMode(false, true, nil, nil) // lag: (true, 2h)
	time.Sleep(100 * time.Millisecond)
	scripted.setMode(true, false, nil, nil) // hang: (true, 0)
	time.Sleep(300 * time.Millisecond)
	scripted.setMode(false, false, nil, nil) // recover: (false, 0)
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}
