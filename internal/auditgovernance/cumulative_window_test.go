package auditgovernance

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestCumulativeWindowExceededBoundary pins the REQ-3 terminal decision as a
// pure function of (firstAttempt, now, window) with injected times — no
// wall-clock wait (AC-3.2/AC-3.4). The five hardening anchors:
//
//  1. Strict boundary: now−firstAttempt == window stays transient; only
//     strictly greater is terminal (FM-10 boundary regression pin).
//  2. Safe direction on anchor loss (FM-3): a zero anchor (row never claimed,
//     pre-0044 row, read-before-first-claim) is never terminal, even with now
//     far past the window.
//  3. Safe direction on clock skew: a negative elapsed (DB clock ahead of the
//     relay clock at claim time) is never terminal — the decision cannot fire
//     before the window has genuinely elapsed.
//  4. Production default window: at MaxBackoffSeconds=300s (config default,
//     pinned separately in the config package) terminality only beyond 300s.
//  5. Window values are positive: the strict-> comparison only ever
//     approaches the boundary from below, so a 2s floor (validation) keeps
//     the safe margins above intact.
func TestCumulativeWindowExceededBoundary(t *testing.T) {
	anchor := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	window := 300 * time.Second
	cases := []struct {
		name  string
		first time.Time
		now   time.Time
		win   time.Duration
		want  bool
	}{
		{"within-window", anchor, anchor.Add(299*time.Second + 999*time.Millisecond), window, false},
		{"exact-boundary-stays-transient", anchor, anchor.Add(window), window, false},
		{"strictly-past-is-terminal", anchor, anchor.Add(window + time.Nanosecond), window, true},
		{"zero-anchor-never-terminal", time.Time{}, time.Now().Add(24 * time.Hour), window, false},
		{"clock-ahead-negative-elapsed", anchor.Add(time.Hour), anchor, window, false},
		{"harness-window-2s-just-past", anchor, anchor.Add(2*time.Second + time.Nanosecond), 2 * time.Second, true},
		{"harness-window-2s-boundary", anchor, anchor.Add(2 * time.Second), 2 * time.Second, false},
		{"production-300s-within", anchor, anchor.Add(299*time.Second + time.Nanosecond), 300 * time.Second, false},
		{"production-300s-past", anchor, anchor.Add(301 * time.Second), 300 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cumulativeWindowExceeded(tc.first, tc.now, tc.win); got != tc.want {
				t.Fatalf("cumulativeWindowExceeded(%v, %v, %v) = %v, want %v",
					tc.first, tc.now, tc.win, got, tc.want)
			}
		})
	}
}

// TestCumulativeWindowDecisionMonotone pins the multi-claim-worker race
// invariant: the decision is monotone in now — once it turns terminal it
// never turns back. Before the first crossing every decision is transient;
// after it every decision is terminal (the strict-> boundary flips exactly
// once, at window). Combined with the fenced retry/fail/complete writes
// (owner+token+live lease), a stale worker holding an expired lease computes
// the same direction as the current holder, so the retry-vs-terminal race
// can only land the same outcome — the fence decides which worker writes it.
func TestCumulativeWindowDecisionMonotone(t *testing.T) {
	anchor := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	window := 2 * time.Second
	seenTerminal := false
	crossings := 0
	at := anchor
	for step := 0; step <= 40; step++ {
		now := cumulativeWindowExceeded(anchor, at, window)
		if now && !seenTerminal {
			crossings++
		}
		if seenTerminal && !now {
			t.Fatalf("decision flipped terminal→transient at %v (monotone violation)", at.Sub(anchor))
		}
		if now {
			if later := cumulativeWindowExceeded(anchor, at.Add(10*time.Minute), window); !later {
				t.Fatalf("decision flipped terminal→transient later at %v", at.Sub(anchor))
			}
		}
		seenTerminal = seenTerminal || now
		at = at.Add(100 * time.Millisecond)
	}
	if !seenTerminal {
		t.Fatal("decision never became terminal across a 4s sweep at window 2s")
	}
	if crossings != 1 {
		t.Fatalf("decision crossed the boundary %d times, want exactly once (strict >)", crossings)
	}
}

// TestRuntimeTransientStreamTerminalizesAfterCumulativeWindow is the AC-3.3
// e2e at the runtime level (the webdav-surface twin belongs to the sibling
// campaign): a receiver answering 500 keeps the row re-POSTed while
// now−anchor <= window (2s harness), then the row goes terminal with the
// full dead-row semantics. Deterministic trace at runtimeConfig
// (initial 1s → max 2s, ±25% jitter): POST#1 ≈ t=0 (claim anchors),
// POST#2 ≈ t∈[0.75,1.25] (within window → retry), POST#3 ≈ t∈[2.3,3.3]
// (window exceeded → terminal, attempts==3). POSTs stop growing thereafter.
func TestRuntimeTransientStreamTerminalizesAfterCumulativeWindow(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "window-terminal.db")
	var posts atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"token","token_type":"Bearer","expires_in":60}`)
			return
		}
		posts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sink.Close()
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	runtime, err := New(runtimeConfig(sink.URL), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	if err := WrapRepository(repo, runtime).RecordAudit(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "tenant.status"}); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	runtime.Start(ctx)
	// POST#3 (the terminal attempt) lands by ~3.3s worst case; 6s deadline
	// absorbs -race/poll slack. The posts counter increments in the sink
	// BEFORE the terminal fail write commits, so the terminal state itself is
	// polled below (never asserted synchronously with the counter).
	pollUntil(t, 6*time.Second, func() bool { return posts.Load() >= 3 })
	if got := posts.Load(); got != 3 {
		t.Fatalf("posts=%d want exactly 3 (claim + 2 retries) before terminal", got)
	}
	// Terminal state, raw SQL (driver registered via the repository package).
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var failedAtNs, attempts int64
	var lastError string
	pollUntil(t, 6*time.Second, func() bool {
		if err := raw.QueryRowContext(ctx,
			`SELECT failed_at_ns, attempts, last_error FROM audit_governance_outbox WHERE tenant_id='acme'`,
		).Scan(&failedAtNs, &attempts, &lastError); err != nil {
			t.Fatalf("read terminal row: %v", err)
		}
		return failedAtNs > 0
	})
	if failedAtNs <= 0 {
		t.Fatalf("failed_at_ns=%d want > 0 (window-terminal)", failedAtNs)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d want 3 (claim + 2 in-window retries)", attempts)
	}
	if lastError == "" {
		t.Fatal("last_error empty on terminal row")
	}
	// No re-POST after terminal: observe ≥ 2× the harness max backoff.
	observeWindow(t, 2600*time.Millisecond)
	if got := posts.Load(); got != 3 {
		t.Fatalf("posts grew after terminal: %d→%d", 3, got)
	}
	// Full dead-row semantics: never re-claimed, absent from lag/Ready.
	if again, err := store.ClaimAuditGovernance(ctx, "observer", "token", 1, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("window-terminal fact reclaimable: len=%d err=%v", len(again), err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("window-terminal fact pending ok=%v err=%v", ok, err)
	}
	runtime.Close()
	if age, ok, err := runtime.PendingBacklogAge(ctx); err != nil || ok {
		t.Fatalf("window-terminal fact in backlog age=%v ok=%v err=%v", age, ok, err)
	}
	if err := runtime.Ready(ctx); err != nil {
		t.Fatalf("window-terminal fact blocks Ready: %v", err)
	}
}

// TestRuntimeMultiWorkerWindowRaceLandsSingleOutcome drives two claim
// identities over one fact with an expired lease — the multi-claim-worker
// retry-vs-terminal race. Worker A claims (anchors), its lease expires while
// its delivery is in flight; worker B re-claims (anchor preserved), and B's
// terminal decision lands. A's late writes must be fenced out: neither A's
// retry nor A's fail can resurrect or re-flip the row, and the row is
// terminal exactly once (attempts == 2, never re-claimed). This pins the
// store half of the race; the monotone-decision invariant (same anchor ⇒
// same direction) is pinned by TestCumulativeWindowDecisionMonotone, and the
// fenced writes are pinned by TestAuditGovernanceAtomicCaptureAndClaimFencing.
func TestRuntimeMultiWorkerWindowRaceLandsSingleOutcome(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-race",
		[]repository.AuditGovernanceBindingState{{TenantID: "acme", State: repository.AuditGovernanceBindingActive}}); err != nil {
		t.Fatal(err)
	}
	fact := repository.AuditGovernanceFact{
		ID: "race-fact", TenantID: "acme", OriginKind: repository.AuditOriginAdmin,
		OriginID: 7, FactKind: "security", Action: "tenant.status",
		OccurredAt: time.Now().UTC(),
	}
	if inserted, err := store.EnqueueAuditGovernance(ctx, fact); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	// Worker A claims with a short lease and never completes (crash/ack-lost).
	a, err := store.ClaimAuditGovernance(ctx, "worker-a", "token-a", 1, 1, 150*time.Millisecond)
	if err != nil || len(a) != 1 {
		t.Fatalf("worker-a claim len=%d err=%v", len(a), err)
	}
	firstAnchor := a[0].FirstAttemptAt
	if firstAnchor.IsZero() {
		t.Fatal("first claim did not anchor first_attempt_at_ns")
	}
	// Worker B cannot re-claim while the lease is live.
	if b, err := store.ClaimAuditGovernance(ctx, "worker-b", "token-b", 1, 1, time.Minute); err != nil || len(b) != 0 {
		t.Fatalf("live lease reclaimed len=%d err=%v", len(b), err)
	}
	time.Sleep(200 * time.Millisecond) // lease (150ms) expires
	// Worker B re-claims: the anchor is idempotent across the lease re-claim.
	b, err := store.ClaimAuditGovernance(ctx, "worker-b", "token-b", 1, 1, time.Minute)
	if err != nil || len(b) != 1 {
		t.Fatalf("worker-b claim len=%d err=%v", len(b), err)
	}
	if b[0].Attempts != 2 {
		t.Fatalf("attempts=%d want 2 after re-claim", b[0].Attempts)
	}
	if !b[0].FirstAttemptAt.Equal(firstAnchor) {
		t.Fatalf("anchor moved across lease re-claim: %v → %v", firstAnchor, b[0].FirstAttemptAt)
	}
	// B lands the terminal write (the only writer with a live fence).
	if err := store.FailAuditGovernance(ctx, b[0].ID, "worker-b", "token-b", "window exceeded"); err != nil {
		t.Fatalf("worker-b fail: %v", err)
	}
	// A's late writes race the terminal state: both must be fenced out.
	if err := store.RetryAuditGovernance(ctx, a[0].ID, "worker-a", "token-a",
		"late retry", time.Now().Add(time.Second)); err == nil {
		t.Fatal("stale worker-a retry landed after worker-b terminal")
	}
	if err := store.FailAuditGovernance(ctx, a[0].ID, "worker-a", "token-a", "late fail"); err == nil {
		t.Fatal("stale worker-a fail landed after worker-b terminal")
	}
	// Exactly one terminal state: never re-claimed, never pending.
	if again, err := store.ClaimAuditGovernance(ctx, "worker-c", "token-c", 1, 1, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("terminal fact reclaimable len=%d err=%v", len(again), err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("terminal fact pending ok=%v err=%v", ok, err)
	}
}
