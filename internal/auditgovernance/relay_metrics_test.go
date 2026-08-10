package auditgovernance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// promHandler serves the Prometheus scrape in this test binary. It is
// installed exactly once in TestMain — each Go test binary has its own
// process globals, so this cannot collide with internal/telemetry's own
// TestMain — and mirrors the single-EnablePrometheus rule of that package.
var promHandler http.Handler

func TestMain(m *testing.M) {
	// Ensure OTel is in no-op mode so EnablePrometheus creates a fresh SDK
	// MeterProvider backed only by Prometheus (no OTLP collector required).
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "") //nolint:errcheck
	var err error
	promHandler, err = telemetry.EnablePrometheus()
	if err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// scrapeValue returns the value of the first line whose series matches name
// ("<name> <value>" or "<name>{labels...} <value>"). Substring matching is
// unsound ("..._total 1" matches "..._total 10"), so every scrape assertion
// goes through this line-exact parse.
func scrapeValue(body, name string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		series := strings.SplitN(fields[0], "{", 2)[0]
		if series != name {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		return v, err == nil
	}
	return 0, false
}

// scrapeProm scrapes the test-binary promHandler and returns the value of one
// counter line (absent series read as 0).
func scrapeProm(t *testing.T, name string) float64 {
	t.Helper()
	if promHandler == nil {
		t.Skip("promHandler not initialized")
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	promHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	value, _ := scrapeValue(rec.Body.String(), name)
	return value
}

// TestRuntimeRelayCountersTrackDeliveryOutcomes proves the AC-3 counter
// wiring end-to-end: a real runtime against three per-binding sinks (success
// 202 / terminal 202+conflict / transient 500) produces the four
// audit_governance.relay_* counters with the contract semantics — exactly one
// delivered and one dead fact (row invariants, immune to retry rounds), and
// >= attempted/failed (the transient tenant may re-claim within the observe
// window). The wire body carries no tenant_id, so the sink routes per-tenant
// via the Authorization header: the /token endpoint issues a distinct token
// per client_id.
func TestRuntimeRelayCountersTrackDeliveryOutcomes(t *testing.T) {
	var succPosts, confPosts, retryPosts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			clientID, _, _ := r.BasicAuth()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w,
				`{"access_token":"token-%s","token_type":"Bearer","expires_in":60}`, clientID)
			return
		}
		auth := r.Header.Get("Authorization")
		var eventID string
		var body struct {
			EventID string `json:"event_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		eventID = body.EventID
		switch {
		case strings.HasPrefix(auth, "Bearer token-succ-client"):
			succPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(w,
				`{"receipt":{"event_id":%q,"tenant_id":"succ","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z"}}`,
				eventID)
		case strings.HasPrefix(auth, "Bearer token-conf-client"):
			confPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(w,
				`{"receipt":{"event_id":%q,"tenant_id":"conf","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}`,
				eventID)
		case strings.HasPrefix(auth, "Bearer token-retry-client"):
			retryPosts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.Error(w, "unknown binding", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "relay-metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeConfig(server.URL)
	cfg.Bindings = []config.AuditGovernanceBinding{
		{TenantID: "succ", ClientID: "succ-client",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_SUCC", State: "active",
			ClientSecret: "succ-machine-secret"},
		{TenantID: "conf", ClientID: "conf-client",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_CONF", State: "active",
			ClientSecret: "conf-machine-secret"},
		{TenantID: "retry", ClientID: "retry-client",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_RETRY", State: "active",
			ClientSecret: "retry-machine-secret"},
	}
	runtime, err := New(cfg, repo.(Store), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	wrapped := WrapRepository(repo, runtime)
	for _, tenant := range []string{"succ", "conf", "retry"} {
		if err := wrapped.RecordAudit(ctx, repository.AuditEntry{
			TenantID: tenant, Action: "key.add",
		}); err != nil {
			t.Fatalf("record audit for %s: %v", tenant, err)
		}
	}

	// Baseline scrape before any relay activity: deltas below are then exact
	// regardless of which other tests ran earlier in this binary.
	attemptedBase := scrapeProm(t, "audit_governance_relay_attempted_total")
	deliveredBase := scrapeProm(t, "audit_governance_relay_delivered_total")
	failedBase := scrapeProm(t, "audit_governance_relay_failed_total")
	deadBase := scrapeProm(t, "audit_governance_relay_dead_total")

	runtime.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for (succPosts.Load() < 1 || confPosts.Load() < 1 || retryPosts.Load() < 1) &&
		time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if succPosts.Load() < 1 || confPosts.Load() < 1 || retryPosts.Load() < 1 {
		t.Fatalf("not all sinks saw a POST: succ=%d conf=%d retry=%d",
			succPosts.Load(), confPosts.Load(), retryPosts.Load())
	}
	runtime.Close()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	promHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	body := rec.Body.String()
	attempted, _ := scrapeValue(body, "audit_governance_relay_attempted_total")
	delivered, _ := scrapeValue(body, "audit_governance_relay_delivered_total")
	failed, _ := scrapeValue(body, "audit_governance_relay_failed_total")
	dead, _ := scrapeValue(body, "audit_governance_relay_dead_total")

	if got := delivered - deliveredBase; got != 1 {
		t.Fatalf("relay_delivered_total delta=%v want 1 (exactly one success fact)", got)
	}
	if got := dead - deadBase; got != 1 {
		t.Fatalf("relay_dead_total delta=%v want 1 (exactly one terminal fact)", got)
	}
	if got := attempted - attemptedBase; got < 3 {
		t.Fatalf("relay_attempted_total delta=%v want >= 3 (one attempt per fact, retries may add)", got)
	}
	if got := failed - failedBase; got < 1 {
		t.Fatalf("relay_failed_total delta=%v want >= 1 (transient fact rescheduled)", got)
	}
	// Reconciliation invariant (D3): attempted >= delivered + failed + dead.
	if attempted < delivered+failed+dead {
		t.Fatalf("relay counters violate invariant: attempted=%v delivered=%v failed=%v dead=%v",
			attempted, delivered, failed, dead)
	}
}
