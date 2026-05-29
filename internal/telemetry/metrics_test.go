package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
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
