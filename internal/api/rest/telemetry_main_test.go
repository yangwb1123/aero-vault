package rest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// sharedRestPromHandler is the /metrics handler installed by TestMain before
// any test in this binary runs. Installing the Prometheus-backed meter
// provider up front is REQUIRED for the counter-surface tests: initDomain
// binds every domain instrument exactly once (domainOnce.Do) to whichever
// MeterProvider is global at first use, and production thumbnail paths
// (cache hits, 304 revalidations) already increment domain counters during
// other tests — without this pre-install the instruments would bind to the
// no-op provider and every scrape would be empty. (A test-time sync.Once
// EnablePrometheus would be too late: the first thumbnail test to run spends
// the once and the fresh provider never sees the already-bound instruments.)
var sharedRestPromHandler http.Handler

// TestMain mirrors internal/telemetry's main_test.go: run EnablePrometheus
// before any test (the package had no TestMain). The OTLP-endpoint env guard
// prevents a CI-injected endpoint from short-circuiting the Prometheus-backed
// provider (EnablePrometheus preserves an already-installed OTLP SDK
// provider).
func TestMain(m *testing.M) {
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "") //nolint:errcheck
	sharedRestPromHandler, _ = telemetry.EnablePrometheus()
	os.Exit(m.Run())
}

// scrapeValue returns the value of the first line whose series matches name
// ("<name> <value>" or "<name>{labels...} <value>"). Local analog of the
// internal/telemetry helper (same name, different package — no import cycle).
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

// scrapeValueLabel is the label-aware variant: it matches the line whose
// series equals name AND whose label set contains labelKey="labelVal".
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

// scrapeMetricsBody runs one /metrics scrape through the shared handler.
func scrapeMetricsBody(t *testing.T) string {
	t.Helper()
	if sharedRestPromHandler == nil {
		t.Skip("sharedRestPromHandler not initialized")
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedRestPromHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	return rec.Body.String()
}
