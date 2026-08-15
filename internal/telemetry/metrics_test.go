package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestDomainMetrics_SurfaceInScrape records each domain metric and verifies it
// appears in the Prometheus scrape body (reusing the shared handler set up once
// in TestMain, per the package's single-EnablePrometheus rule).
func TestDomainMetrics_SurfaceInScrape(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	ctx := context.Background()

	RecordAIUsage(ctx, "acme", "gpt-test", 100, 50, 500_000)
	RecordEmbedUsage(ctx, "embed-test", 320)
	RecordSearchLatency(ctx, "hybrid", 12.5)
	RecordEmbedLatency(ctx, "embed-test", 8.0)
	RecordReconcileBlobs(ctx, 3, 2)
	IncIdempotencyReplay(ctx)
	IncEventDropped(ctx)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	body := rec.Body.String()

	// OTel counters are exported with a _total suffix and dots → underscores.
	want := []string{
		"ai_requests",
		"ai_tokens",
		"ai_cost_micros",
		"ai_embed_requests",
		"ai_embed_tokens",
		"ai_search_duration_ms",
		"ai_embed_duration_ms",
		"reconcile_orphan_blobs",
		"idempotency_replays",
		"events_dropped",
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("expected scrape body to contain %q", w)
		}
	}
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

// scrapeValueLabel is the label-aware variant of scrapeValue: it matches the
// line whose series equals name AND whose label set contains labelKey=labelVal
// (e.g. ai_search_degraded_total{reason="embed"} 1). Plain scrapeValue only
// matches the series name and returns the first line, so per-label assertions
// must go through this helper.
func scrapeValueLabel(body, name, labelKey, labelVal string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		series := strings.SplitN(fields[0], "{", 2)[0]
		if series != name {
			continue
		}
		if !strings.Contains(fields[0], labelKey+"=\""+labelVal+"\"") {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		return v, err == nil
	}
	return 0, false
}

// TestAuditGovernanceMetrics_SurfaceInScrape records each audit-governance
// relay counter once and verifies the four names/values appear in the scrape
// (AC-3 surface half). Only this test increments these counters in this test
// binary, so the absolute value 1 is exact; the line-exact scrapeValue guard
// pins the exported names (dots → underscores, _total suffix) against drift.
func TestAuditGovernanceMetrics_SurfaceInScrape(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	ctx := context.Background()
	IncAuditGovernanceRelayAttempted(ctx)
	IncAuditGovernanceRelayDelivered(ctx)
	IncAuditGovernanceRelayFailed(ctx)
	IncAuditGovernanceRelayDead(ctx)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	body := rec.Body.String()
	for _, name := range []string{
		"audit_governance_relay_attempted_total",
		"audit_governance_relay_delivered_total",
		"audit_governance_relay_failed_total",
		"audit_governance_relay_dead_total",
	} {
		value, ok := scrapeValue(body, name)
		if !ok || value != 1 {
			t.Errorf("scrape %s: value=%v ok=%v, want 1", name, value, ok)
		}
	}
}

// TestSearchDegradedMetrics_SurfaceInScrape records the degraded-search
// counter once per reason and verifies each labeled series appears in the
// scrape with the exact value 1 (AC-4). Only this test increments these
// counters in this binary, so the absolute value is exact; scrapeValueLabel
// pins the exported name (dots → underscores, _total suffix) and the fixed
// 3-value reason label set against drift.
func TestSearchDegradedMetrics_SurfaceInScrape(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	ctx := context.Background()
	IncSearchDegraded(ctx, "embed")
	IncSearchDegraded(ctx, "vector")
	IncSearchDegraded(ctx, "lexical")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	body := rec.Body.String()
	for _, reason := range []string{"embed", "vector", "lexical"} {
		value, ok := scrapeValueLabel(body, "ai_search_degraded_total", "reason", reason)
		if !ok || value != 1 {
			t.Errorf("scrape ai_search_degraded_total{reason=%q}: value=%v ok=%v, want 1", reason, value, ok)
		}
	}
}

// TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape registers the backlog-age
// gauge exactly once (single-shot registration — OTel rejects a duplicate
// instrument on the same meter) with a captured callback and pins the exported
// line through the shared scrape handler; re-scraping after the callback flips
// proves the observable value follows the callback (the cache-fed D3 seam).
func TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	age := int64(137)
	RegisterAuditGovernanceBacklogAgeGauge(func(context.Context) int64 { return age })

	body := scrapeShared(t)
	if v, ok := scrapeValue(body, "audit_governance_backlog_age_seconds"); !ok || v != 137 {
		t.Fatalf("age gauge: value=%v ok=%v want 137", v, ok)
	}
	age = 0
	body = scrapeShared(t)
	if v, ok := scrapeValue(body, "audit_governance_backlog_age_seconds"); !ok || v != 0 {
		t.Fatalf("age gauge after flip: value=%v ok=%v want 0", v, ok)
	}
}

// TestAuditGovernanceDegradedGaugeSurfaceInScrape registers the degraded-flag
// gauge exactly once and pins the 0/1 encoding (the F11/F16 alert arm): 1 when
// the callback reports degraded, 0 when healthy — line-exact via scrapeValue.
func TestAuditGovernanceDegradedGaugeSurfaceInScrape(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	degraded := int64(1)
	RegisterAuditGovernanceDegradedGauge(func(context.Context) int64 { return degraded })

	body := scrapeShared(t)
	if v, ok := scrapeValue(body, "audit_governance_degraded"); !ok || v != 1 {
		t.Fatalf("degraded gauge: value=%v ok=%v want 1", v, ok)
	}
	degraded = 0
	body = scrapeShared(t)
	if v, ok := scrapeValue(body, "audit_governance_degraded"); !ok || v != 0 {
		t.Fatalf("degraded gauge after flip: value=%v ok=%v want 0", v, ok)
	}
}

// TestAuditGovernanceDrainGaugesSurfaceInScrape registers the drain-mode
// gauge pair exactly once and pins both exported lines through the shared
// scrape handler (rule-3 observability): bound_tenants and draining follow
// the stub callback — the only positive signal distinguishing a
// drained-but-enabled relay (0, 1) from a healthy one (N>0, 0).
func TestAuditGovernanceDrainGaugesSurfaceInScrape(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	bound := int64(0)
	draining := int64(1)
	RegisterAuditGovernanceDrainGauges(func(context.Context) (int64, int64) {
		return bound, draining
	})

	body := scrapeShared(t)
	if v, ok := scrapeValue(body, "audit_governance_bound_tenants"); !ok || v != 0 {
		t.Fatalf("bound_tenants gauge: value=%v ok=%v want 0", v, ok)
	}
	if v, ok := scrapeValue(body, "audit_governance_draining"); !ok || v != 1 {
		t.Fatalf("draining gauge: value=%v ok=%v want 1", v, ok)
	}
	bound, draining = 2, 0
	body = scrapeShared(t)
	if v, ok := scrapeValue(body, "audit_governance_bound_tenants"); !ok || v != 2 {
		t.Fatalf("bound_tenants gauge after flip: value=%v ok=%v want 2", v, ok)
	}
	if v, ok := scrapeValue(body, "audit_governance_draining"); !ok || v != 0 {
		t.Fatalf("draining gauge after flip: value=%v ok=%v want 0", v, ok)
	}
}

// TestThumbnailCacheCountersSurface records the server-side thumbnail cache
// counters and verifies they appear in the Prometheus scrape body with the
// expected values (reusing the shared handler set up once in TestMain).
func TestThumbnailCacheCountersSurface(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	ctx := context.Background()
	IncThumbnailCacheHit(ctx)
	IncThumbnailCacheHit(ctx)
	IncThumbnailCacheMiss(ctx)
	IncThumbnailCacheEviction(ctx, 3)

	body := scrapeShared(t)
	if v, ok := scrapeValue(body, "thumbnail_cache_hits_total"); !ok || v != 2 {
		t.Fatalf("thumbnail_cache_hits_total = %v (ok=%v), want 2", v, ok)
	}
	if v, ok := scrapeValue(body, "thumbnail_cache_misses_total"); !ok || v != 1 {
		t.Fatalf("thumbnail_cache_misses_total = %v (ok=%v), want 1", v, ok)
	}
	if v, ok := scrapeValue(body, "thumbnail_cache_evictions_total"); !ok || v != 3 {
		t.Fatalf("thumbnail_cache_evictions_total = %v (ok=%v), want 3", v, ok)
	}
}

// TestThumbnail304CounterSurface records the thumbnail 304 revalidation
// counter and verifies it appears in the Prometheus scrape body with the
// expected value (reusing the shared handler set up once in TestMain).
func TestThumbnail304CounterSurface(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	ctx := context.Background()
	IncThumbnail304(ctx)
	IncThumbnail304(ctx)

	body := scrapeShared(t)
	if v, ok := scrapeValue(body, "thumbnail_304_total"); !ok || v != 2 {
		t.Fatalf("thumbnail_304_total = %v (ok=%v), want 2", v, ok)
	}
}

// scrapeShared runs one /metrics scrape through the shared handler.
func scrapeShared(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	return rec.Body.String()
}

// TestObservableGauges_SurfaceInScrape registers the queue-depth and storage
// gauges with fixed callbacks and asserts they're collected on scrape.
func TestObservableGauges_SurfaceInScrape(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	RegisterQueueDepthGauge(func(context.Context) int64 { return 7 })
	RegisterStorageGauges(func(context.Context) []TenantStorage {
		return []TenantStorage{{Tenant: "acme", Bytes: 4096, Objects: 3}}
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	body := rec.Body.String()
	for _, w := range []string{"jobs_pending", "storage_bytes", "storage_objects"} {
		if !strings.Contains(body, w) {
			t.Errorf("expected scrape body to contain %q", w)
		}
	}
}
