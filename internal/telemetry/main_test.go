package telemetry

import (
	"os"
	"testing"
)

// TestMain is the entry point for the telemetry test binary. It calls
// EnablePrometheus() exactly once so that the global prometheus.DefaultRegisterer
// is not hit with duplicate registrations across individual Test* functions.
func TestMain(m *testing.M) {
	if os.Getenv("AERO_TELEMETRY_SKIP_TESTMAIN_PROM") != "1" {
		// Ensure OTel is in no-op mode so EnablePrometheus creates a fresh
		// SDK MeterProvider backed only by Prometheus (no OTLP collector required).
		os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "") //nolint:errcheck
		sharedPromHandler, sharedPromErr = EnablePrometheus()
	}
	os.Exit(m.Run())
}
