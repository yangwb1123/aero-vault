package telemetry

// NOTE on test design:
// EnablePrometheus() registers metrics with the global prometheus.DefaultRegisterer.
// Re-registering the same collector (target_info, etc.) causes a 500 scrape error
// because Go's prometheus library panics/errors on duplicate registration. Therefore
// all Prometheus tests MUST share a single EnablePrometheus() call. We do this with
// TestMain + a package-level setup, calling EnablePrometheus() exactly once and
// reusing the resulting handler across every sub-test.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// promOnce holds the result of the single EnablePrometheus() call shared across
// all tests in this file. Populated by TestMain in main_test.go.
var (
	sharedPromHandler http.Handler
	sharedPromErr     error
)

// TestEnablePrometheus_ReturnsHandler verifies that EnablePrometheus returned a
// non-nil http.Handler and no error.
func TestEnablePrometheus_ReturnsHandler(t *testing.T) {
	if sharedPromErr != nil {
		t.Fatalf("EnablePrometheus returned unexpected error: %v", sharedPromErr)
	}
	if sharedPromHandler == nil {
		t.Fatal("EnablePrometheus returned nil handler")
	}
}

// TestEnablePrometheus_MetricsEndpointResponds verifies that the handler
// serves a valid HTTP 200 response.
func TestEnablePrometheus_MetricsEndpointResponds(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d\nbody: %s", rec.Code, rec.Body.String())
	}
}

// TestEnablePrometheus_BodyContainsGoMetrics verifies that the scrape body
// includes standard Go runtime metrics (go_ prefix).
func TestEnablePrometheus_BodyContainsGoMetrics(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "go_") {
		t.Errorf("expected scrape body to contain 'go_' metrics, got:\n%s", body)
	}
}

// TestEnablePrometheus_AfterHTTPRequests verifies the full pipeline:
//  1. EnablePrometheus installed a Prometheus reader (done once in TestMain).
//  2. HTTPMiddleware increments OTel counters on each request.
//  3. The scrape body contains OTel-derived http server metric names.
func TestEnablePrometheus_AfterHTTPRequests(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}

	// Fire requests through the middleware to generate metric data.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := HTTPMiddleware("aero-vault")
	wrapped := mw(inner)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
	}

	// Scrape Prometheus.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d\nbody: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	// Standard runtime metrics must be present.
	if !strings.Contains(body, "go_") {
		t.Errorf("expected 'go_' runtime metrics in scrape body")
	}

	// OTel-derived HTTP server metrics should appear after requests.
	// OTel translates "http.server.requests" → "http_server_requests_total"
	// and "http.server.duration_ms" → "http_server_duration_ms".
	wantSubstrings := []string{
		"http_server_requests",
		"http_server_duration_ms",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Logf("scrape body:\n%s", body)
			t.Errorf("expected scrape body to contain %q", want)
		}
	}
}

// TestEnablePrometheus_ContentTypeIsText verifies the scrape response uses
// text/plain content type (Prometheus text exposition format).
func TestEnablePrometheus_ContentTypeIsText(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedPromHandler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected Content-Type to start with 'text/plain', got %q", ct)
	}
}
