package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSetup_MixedModeExportsDomainMetricsToPrometheusAndOTLP(t *testing.T) {
	if os.Getenv("AERO_TELEMETRY_SUBPROCESS_MODE") == "mixed" {
		testMixedModeHelper(t)
		return
	}
	runTelemetrySubprocess(t, "mixed", "^TestSetup_MixedModeExportsDomainMetricsToPrometheusAndOTLP$")
}

func TestEnablePrometheus_FailsClosedAfterOTLPOnlySetup(t *testing.T) {
	if os.Getenv("AERO_TELEMETRY_SUBPROCESS_MODE") == "late-prom" {
		testLatePrometheusHelper(t)
		return
	}
	runTelemetrySubprocess(t, "late-prom", "^TestEnablePrometheus_FailsClosedAfterOTLPOnlySetup$")
}

func runTelemetrySubprocess(t *testing.T, mode, testName string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+testName)
	cmd.Env = append(os.Environ(),
		"AERO_TELEMETRY_SKIP_TESTMAIN_PROM=1",
		"AERO_TELEMETRY_SUBPROCESS_MODE="+mode,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess %s failed: %v\n%s", mode, err, out)
	}
}

func testMixedModeHelper(t *testing.T) {
	var metricPosts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/metrics" {
			metricPosts.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	ctx := context.Background()
	shutdown, err := Setup(ctx, "aero-vault", discardLogger(), true)
	if err != nil {
		t.Fatalf("Setup mixed mode: %v", err)
	}

	h, err := EnablePrometheus()
	if err != nil {
		t.Fatalf("EnablePrometheus after mixed setup: %v", err)
	}
	if h == nil {
		t.Fatal("EnablePrometheus returned nil handler")
	}

	SetThumbnailCacheSweepCadence(ctx, 17, 23)
	body := scrapeHandler(t, h)
	if v, ok := scrapeValue(body, "thumbnail_cache_sweep_interval_seconds"); !ok || v != 17 {
		t.Fatalf("thumbnail_cache_sweep_interval_seconds: value=%v ok=%v want 17", v, ok)
	}
	if v, ok := scrapeValue(body, "thumbnail_cache_sweep_last_run_seconds"); !ok || v != 23 {
		t.Fatalf("thumbnail_cache_sweep_last_run_seconds: value=%v ok=%v want 23", v, ok)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown mixed mode: %v", err)
	}
	waitForMetricPost(t, &metricPosts)
}

func testLatePrometheusHelper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	ctx := context.Background()
	shutdown, err := Setup(ctx, "aero-vault", discardLogger(), false)
	if err != nil {
		t.Fatalf("Setup OTLP-only: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown OTLP-only: %v", err)
		}
	}()

	h, err := EnablePrometheus()
	if err == nil {
		t.Fatal("EnablePrometheus unexpectedly succeeded after OTLP-only setup")
	}
	if h != nil {
		t.Fatalf("EnablePrometheus returned handler %T on fail-closed path", h)
	}
	if !strings.Contains(err.Error(), "must be enabled during telemetry setup") {
		t.Fatalf("EnablePrometheus error = %q, want setup-time guidance", err.Error())
	}
}

func scrapeHandler(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d\n%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func waitForMetricPost(t *testing.T, metricPosts *atomic.Int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if metricPosts.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("OTLP metric exporter did not POST /v1/metrics; count=%d", metricPosts.Load()))
}
