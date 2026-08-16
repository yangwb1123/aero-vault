package main

// TestMain for the cmd/server package: installs the Prometheus exporter once
// so the thumbnail sweep driver's telemetry forwarding can be asserted
// through the real scrape surface (QA P1 — the production path, not a
// manually-incremented unit counter). Mirrors the internal/api/rest
// telemetry_main_test.go pattern.
import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/telemetry"
)

var sharedCmdPromHandler http.Handler

func TestMain(m *testing.M) {
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "") //nolint:errcheck
	sharedCmdPromHandler, _ = telemetry.EnablePrometheus()
	os.Exit(m.Run())
}

// cmdScrapeValue returns the value of the first /metrics line whose series
// name matches name.
func cmdScrapeValue(t *testing.T, name string) (float64, bool) {
	t.Helper()
	if sharedCmdPromHandler == nil {
		t.Skip("sharedCmdPromHandler not initialized")
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	sharedCmdPromHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.SplitN(fields[0], "{", 2)[0] != name {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		return v, err == nil
	}
	return 0, false
}
