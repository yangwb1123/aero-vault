package telemetry

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

// discardLogger returns a *slog.Logger that discards all output, suitable for
// use in tests where log output is not under test.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.NewFile(0, os.DevNull), nil))
}

// TestSetup_NoEndpoint verifies that when OTEL_EXPORTER_OTLP_ENDPOINT is empty,
// Setup returns a non-nil shutdown func and no error without requiring a real
// OTLP collector.
func TestSetup_NoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	ctx := context.Background()
	shutdown, err := Setup(ctx, "aero-vault", discardLogger(), false)
	if err != nil {
		t.Fatalf("expected no error from Setup with no endpoint, got: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown func, got nil")
	}
}

// TestSetup_ShutdownNoError verifies that calling the returned shutdown func
// when no real collector is configured does not return an error.
func TestSetup_ShutdownNoError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	ctx := context.Background()
	shutdown, err := Setup(ctx, "aero-vault", discardLogger(), false)
	if err != nil {
		t.Fatalf("unexpected Setup error: %v", err)
	}

	if shutdownErr := shutdown(ctx); shutdownErr != nil {
		t.Fatalf("unexpected shutdown error: %v", shutdownErr)
	}
}

// TestSetup_EmptyServiceName verifies that an empty service name is accepted
// (the code defaults it to "aero-vault" only when the OTLP path is taken, but
// the no-op path should still succeed).
func TestSetup_EmptyServiceName(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	ctx := context.Background()
	shutdown, err := Setup(ctx, "", discardLogger(), false)
	if err != nil {
		t.Fatalf("expected no error with empty service name, got: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown func, got nil")
	}
}

// TestSetup_ShutdownType verifies that the returned Shutdown satisfies the
// Shutdown type defined in the package (func(context.Context) error).
func TestSetup_ShutdownType(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	ctx := context.Background()
	var s Shutdown
	var err error
	s, err = Setup(ctx, "svc", discardLogger(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Call through the typed alias.
	if err := s(ctx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
}

// TestSetup_CancelledContext verifies Setup is well-behaved when the context
// passed in is already cancelled (no-op path should not care about ctx state).
func TestSetup_CancelledContext(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	shutdown, err := Setup(ctx, "aero-vault", discardLogger(), false)
	if err != nil {
		t.Fatalf("expected no error for no-op path even with cancelled ctx, got: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown func")
	}
}
