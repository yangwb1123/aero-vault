package auditgovernance

// runtime_gauge_scrape_test.go pins the B3-4 gauge seam end-to-end: the
// OTel-exported audit_governance_backlog_age_seconds line, scraped through
// the binary's Prometheus handler, is fed by a REAL runtime's degraded cache
// via the production-identical truncating callback (int64 seconds, exactly
// cmd/server build.go's auditGovernanceBacklogAgeGaugeFn shape) — rising
// above zero on a backdated pending fact and falling to exactly zero when
// only dead rows remain. A regression that zeroes the gauge would silently
// kill the 450s degraded alert (alerts.yml) with every other probe green, so
// both directions are asserted. Probes are driven by Ready(), never Start():
// the run loop would claim and retry the pending fact (the T6 relay-claim
// race), while this test only needs the cache-fed gauge seam.

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime pins the B3-4 acceptance:
// scrape of the registered observable gauge fed by a real runtime — stale
// pending fact (16s backdate, > maxLag 4s) → gauge > 0; Claim + Fail leaves
// only dead rows → gauge == 0 exactly; and the REQ-2 panic phase proving the
// gauge callback reads the cache, never the store (armed scriptedStore).
func TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime(t *testing.T) {
	if promHandler == nil {
		t.Skip("promHandler not initialized")
	}
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "gauge-scrape.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	scripted := &scriptedStore{store: store}
	rt, err := New(runtimeConfig("http://127.0.0.1:1"), scripted,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	now := time.Now().UTC()
	if _, err := rt.store.InsertEventWithGovernance(ctx, repository.Event{
		TenantID: "acme", Bucket: "b", Key: "k", Type: repository.EventCreated,
		CreatedAt: now,
	}, repository.AuditGovernanceFact{SourceID: "acme", TenantID: "acme",
		OriginKind: repository.AuditOriginFile, FactKind: "file",
		Action: "file.create", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	backdatePendingFact(t, dsn, 16*time.Second)

	// Single-shot registration per meter: no other test in this binary
	// registers the backlog-age gauge, so the callback below is the live one.
	// Truncating callback mirrors auditGovernanceBacklogAgeGaugeFn exactly
	// (D4): int64 seconds, no rounding — 16s+ backdate truncates to 16.
	telemetry.RegisterAuditGovernanceBacklogAgeGauge(func(context.Context) int64 {
		return int64(rt.BacklogAge().Seconds())
	})

	// Stale pending fact: Ready() records degraded (age 16s > maxLag 4s), and
	// the scrape must surface it — the alert source.
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready=%v, want nil (lag degrades, never fails)", err)
	}
	if got := rt.BacklogAge(); got <= 4*time.Second {
		t.Fatalf("cache BacklogAge()=%v, want > maxLag (4s)", got)
	}
	if v := scrapeProm(t, "audit_governance_backlog_age_seconds"); v <= 0 {
		t.Fatalf("gauge=%v want > 0 while a stale fact is pending", v)
	}

	// REQ-2 panic phase: the callback exercised by the next scrape must read
	// the cache, never the store. Arm the scripted store's backlog probe so
	// that ANY store query from the callback panics (test failure = loud).
	// No further Ready()/Start() after arming — the cache is frozen at the
	// last-probe value (16s+), which is exactly the semantics under test: a
	// regression re-adding a store query to the registered callback crashes
	// here, deterministically.
	scripted.setPanicBacklog(true)
	want := float64(int64(rt.BacklogAge().Seconds()))
	if v := scrapeProm(t, "audit_governance_backlog_age_seconds"); v != want {
		t.Fatalf("gauge=%v want %v (cached last-probe value; callback must not query the store)", v, want)
	}
	scripted.setMode(false, false, nil, nil) // clear panicBacklog before any more probes

	// Dead-only: Claim + Fail via the public lease-fenced API — terminal rows
	// are excluded by the store query, so the gauge must return to exactly 0.
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
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready=%v, want nil with only dead rows", err)
	}
	if got := rt.BacklogAge(); got != 0 {
		t.Fatalf("cache BacklogAge()=%v want 0 (dead rows excluded)", got)
	}
	if v := scrapeProm(t, "audit_governance_backlog_age_seconds"); v != 0 {
		t.Fatalf("gauge=%v want exactly 0 when only dead rows remain", v)
	}
}
