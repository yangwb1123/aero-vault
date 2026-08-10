package auditgovernance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// terminalCase is one row of the REQ-5.1 table: the sink behavior that must
// land a posted fact in the terminal failed state (failed_at_ns>0, exactly
// one POST, never re-claimed, absent from the pending/lag scan).
type terminalCase struct {
	name      string
	status    int
	respond   func(http.ResponseWriter, *http.Request)
	retention bool // 409 case additionally pins the retention prune.
}

// TestRuntimePermanentDeliveryErrorsAreTerminal covers the four permanent
// classes (REQ-5.1): HTTP 409/422 (status only, no body), tenant-mismatch
// receipt, non-ledgered receipt, unparseable receipt body. Table-driven with
// t.Run + t.Parallel — every case runs against its own httptest server and
// tempdir DB, so the wall-clock cost is a single observe window, not 5x.
func TestRuntimePermanentDeliveryErrorsAreTerminal(t *testing.T) {
	cases := []terminalCase{
		{name: "http409", status: http.StatusConflict, retention: true},
		{name: "http422", status: http.StatusUnprocessableEntity},
		{name: "tenant-mismatch", status: http.StatusAccepted, respond: tenantMismatchReceipt},
		{name: "non-ledgered-status", status: http.StatusAccepted, respond: nonLedgeredReceipt},
		{name: "unparseable-body", status: http.StatusAccepted, respond: unparseableReceipt},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTerminalCase(t, tc)
		})
	}
}

// runTerminalCase drives one table row end-to-end: sink → runtime → first
// POST (3 s deadline) → observe 2.6 s with the runtime still running → Close
// → terminal assertions. The observe window exceeds the harness max backoff
// (2 s) + poll slack, so a misclassified-transient row (re-POST at
// [0.75, 1.25] s, worst case ~1.3 s) would be caught by posts==1.
func runTerminalCase(t *testing.T, tc terminalCase) {
	t.Helper()
	ctx := context.Background()
	var posts atomic.Int32
	sink := terminalSink(t, tc, &posts)
	defer sink.Close()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), tc.name+".db"))
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
	pollUntil(t, 3*time.Second, func() bool { return posts.Load() >= 1 })
	if posts.Load() != 1 {
		t.Fatalf("first POST never happened: posts=%d", posts.Load())
	}
	observeWindow(t, 2600*time.Millisecond)
	runtime.Close()
	assertTerminalState(t, runtime, store, &posts)
	if tc.retention {
		assertTerminalRetention(t, store)
	}
}

// pollUntil waits on a 10 ms cadence until done or the deadline — the
// existing harness pattern from runtime_test.go.
func pollUntil(t *testing.T, deadline time.Duration, done func() bool) {
	t.Helper()
	until := time.Now().Add(deadline)
	for !done() && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
}

// observeWindow keeps the runtime running for at least window (10 ms cadence).
func observeWindow(t *testing.T, window time.Duration) {
	t.Helper()
	until := time.Now().Add(window)
	for time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
}

// assertTerminalState pins the REQ-5.1 anchors after Close: exactly one POST,
// never re-claimed (claim predicate excludes failed_at_ns != 0), absent from
// the lag scan (OldestPendingAuditGovernance also excludes failed rows), and
// the T-3 runtime surface over the same dead rows — Ready()==nil, not
// degraded, zero backlog age. The Ready() trio is the T-3 table pin (distinct
// from the store-API probes above): a pending-row regression shows up in
// BacklogAge()>0 for EVERY timing (assert time ∈ [2.6, 5.6]s after fact
// creation, so the pending row is always older than 0), while Degraded() only
// discriminates past maxLag (4s) and Ready()==nil never discriminates — all
// three assertions are required. Ready() after Close() is race-free: the run
// loop is done and probeAndRecord only touches the store + degraded cache.
func assertTerminalState(t *testing.T, rt *Runtime, store Store, posts *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	if got := posts.Load(); got != 1 {
		t.Fatalf("fact re-POSTed %d times, want exactly 1 (terminal)", got)
	}
	if again, err := store.ClaimAuditGovernance(ctx, "observer", "token", 1, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("terminal fact reclaimable: len=%d err=%v", len(again), err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("terminal fact counts as pending ok=%v err=%v", ok, err)
	}
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready=%v, want nil (dead rows never block readiness)", err)
	}
	if rt.Degraded() {
		t.Fatal("Degraded()=true, want false with a dead-lettered backlog")
	}
	if got := rt.BacklogAge(); got != 0 {
		t.Fatalf("BacklogAge()=%v want 0 (terminal rows excluded from the lag scan)", got)
	}
}

// assertTerminalRetention pins terminal-with-retention: the failed row
// survives until the retention window, then is pruned (verbatim pattern of
// runtime_test.go:180-186).
func assertTerminalRetention(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	if n, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(-time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("failed row pruned before retention window: n=%d err=%v", n, err)
	}
	if n, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(time.Hour), 10); err != nil || n != 1 {
		t.Fatalf("failed row not pruned after retention window: n=%d err=%v", n, err)
	}
}

// terminalSink serves /token and answers fact POSTs per the table row. 409/422
// answer status-only (non-202 → the publisher's httpStatusError before any
// body read); 202 rows answer with tc.respond's body. posts counts POSTs.
func terminalSink(t *testing.T, tc terminalCase, posts *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
			return
		}
		posts.Add(1)
		if tc.status != http.StatusAccepted {
			w.WriteHeader(tc.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if tc.respond != nil {
			tc.respond(w, r)
		}
	}))
}

// tenantMismatchReceipt echoes the posted event_id with a wrong tenant_id:
// receiptMatches fails on TenantID (http.go:217), not EventID — isolating the
// tenant branch of ErrInvalidReceipt.
func tenantMismatchReceipt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventID string `json:"event_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":%q,"tenant_id":"other","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z"}}`, body.EventID)
}

// nonLedgeredReceipt echoes the posted event_id with a status the receiver's
// predicate rejects (http.go:222-225).
func nonLedgeredReceipt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventID string `json:"event_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":%q,"tenant_id":"acme","status":"rejected","accepted_at":"2026-08-04T00:00:00Z"}}`, body.EventID)
}

// unparseableReceipt answers a JSON-shaped 202 with a non-JSON body
// (http.go:194).
func unparseableReceipt(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "not-json")
}

// TestIsPermanentDeliveryErrorClosedList pins the D1 closed list in both
// directions (REQ-5.1a): the permanent set exactly, and the transient set —
// anything not listed is transient by construction, so a future edit adding a
// permanent class without updating the classifier fails here.
func TestIsPermanentDeliveryErrorClosedList(t *testing.T) {
	permanent := []error{
		ErrReceiptConflict,
		ErrInvalidReceipt,
		&httpStatusError{Status: http.StatusConflict},
		&httpStatusError{Status: http.StatusUnprocessableEntity},
	}
	for _, err := range permanent {
		if !isPermanentDeliveryError(fmt.Errorf("%w: wrapped", err)) {
			t.Errorf("permanent error %T(%v) classified transient", err, err)
		}
	}
	transient := []error{
		&httpStatusError{Status: http.StatusBadRequest},
		&httpStatusError{Status: http.StatusUnauthorized},
		&httpStatusError{Status: http.StatusForbidden},
		&httpStatusError{Status: http.StatusNotFound},
		&httpStatusError{Status: http.StatusGone},
		&httpStatusError{Status: http.StatusTooManyRequests},
		&httpStatusError{Status: http.StatusInternalServerError},
		&httpStatusError{Status: http.StatusNotImplemented},
		&httpStatusError{Status: http.StatusServiceUnavailable},
		ErrInvalidEvent,
		ErrTokenUnavailable,
		errors.New("transport reset"),
		context.DeadlineExceeded,
	}
	for _, err := range transient {
		if isPermanentDeliveryError(err) {
			t.Errorf("transient error %T(%v) classified permanent", err, err)
		}
	}
}

// TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff pins REQ-2 AC-2.4
// e2e: a transient 5xx fact is POSTed more than once with the runtime still
// running, and the inter-POST gaps grow. Growth is the deterministic proxy
// for available_at_ns strictly increasing between retries: boundedBackoff
// advances available_at_ns by at least initial/2 (> 0) on every retry, and at
// the harness config (initial 1s → max 2s, ±25 % per-ID jitter) gap₁ ∈
// [0.75, 1.25] s while gap₂ ∈ [1.5, 2.0] s — min(gap₂) > max(gap₁), so the
// assertion holds for every fact ID on every run. The harness window is also
// 2s, so the cumulative cap terminates the stream at POST#3: the growth pin
// remains the anchor (the terminal e2e is
// TestRuntimeTransientStreamTerminalizesAfterCumulativeWindow).
func TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff(t *testing.T) {
	ctx := context.Background()
	var posts atomic.Int32
	var mu sync.Mutex
	var gaps []time.Duration
	var last time.Time
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"token","token_type":"Bearer","expires_in":60}`)
			return
		}
		now := time.Now()
		mu.Lock()
		if posts.Load() > 0 {
			gaps = append(gaps, now.Sub(last))
		}
		last = now
		mu.Unlock()
		posts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sink.Close()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "transient-repost.db"))
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
	pollUntil(t, 3*time.Second, func() bool { return posts.Load() >= 1 })
	// Observe ≥ 2 full backoff windows (worst case POST#3 at ~3.25 s + poll
	// slack): guarantees both the re-POST count and two measurable gaps.
	observeWindow(t, 5*time.Second)
	if got := posts.Load(); got < 2 {
		t.Fatalf("transient fact POSTed %d times, want ≥ 2 (bounded retry, not terminal)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gaps) < 2 {
		t.Fatalf("only %d inter-POST gaps measured (posts=%d), want ≥ 2", len(gaps), posts.Load())
	}
	if gaps[1] <= gaps[0] {
		t.Fatalf("backoff did not grow: gap1=%v gap2=%v (want gap2 > gap1)", gaps[0], gaps[1])
	}
	runtime.Close()
	// The harness window (2s) is now exceeded by POST#3 (≈[2.3,3.3]s), so the
	// row is window-terminal: never re-claimed, absent from the lag scan —
	// the same dead-row probes as the terminal e2e.
	if again, err := store.ClaimAuditGovernance(ctx, "observer", "token", 1, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("window-terminal fact reclaimable: len=%d err=%v", len(again), err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("window-terminal fact pending ok=%v err=%v", ok, err)
	}
}
