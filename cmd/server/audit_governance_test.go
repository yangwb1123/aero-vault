package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestDisabledAuditGovernanceRequiresPersistedBindingsRemoved(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "disabled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	if runtime, err := buildAuditGovernanceRuntime(cfg, repo, logger); err != nil || runtime != nil {
		t.Fatalf("fresh disabled runtime=%v err=%v", runtime, err)
	}
	store := repo.(repository.AuditGovernanceStore)
	binding := []repository.AuditGovernanceBindingState{{
		TenantID: "acme", State: repository.AuditGovernanceBindingActive,
	}}
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "active-digest", binding); err != nil {
		t.Fatal(err)
	}
	if _, err := buildAuditGovernanceRuntime(cfg, repo, logger); err == nil {
		t.Fatal("disabled startup accepted a persisted binding")
	}
	if err := store.ApplyAuditGovernanceBindings(ctx, 2, "empty-digest", nil); err != nil {
		t.Fatal(err)
	}
	if runtime, err := buildAuditGovernanceRuntime(cfg, repo, logger); err != nil || runtime != nil {
		t.Fatalf("drained disabled runtime=%v err=%v", runtime, err)
	}
}

// TestBuildAuditGovernanceRuntimeDrainBootLogsWarn pins rule 3's per-boot
// WARN: a drain-mode boot (AUDIT_GOVERNANCE_DRAIN + empty manifest) logs a
// distinct WARN naming the flag and the applied revision — healthy boots
// keep the INFO line — so the drained-but-enabled state is never silent.
func TestBuildAuditGovernanceRuntimeDrainBootLogsWarn(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "drainboot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	cfg := &config.Config{AuditGovernance: config.AuditGovernanceConfig{
		Enabled: true, Drain: true, BaseURL: "http://127.0.0.1:1",
		TokenURL: "http://127.0.0.1:1/token",
		HMACKey:  "audit-governance-hmac-key-32-bytes-minimum", Revision: 2,
		HTTPTimeoutSeconds: 1, PollMilliseconds: 10, BatchSize: 10, ClaimTTLSeconds: 3,
		InitialBackoffSeconds: 1, MaxBackoffSeconds: 2, MaxLagSeconds: 4,
		ReconcileBatchSize: 20, DeliveredRetentionSeconds: 3600,
		CleanupIntervalSeconds: 60, CleanupBatchSize: 20,
	}}
	runtime, err := buildAuditGovernanceRuntime(cfg, repo, logger)
	if err != nil {
		t.Fatalf("drain boot: %v", err)
	}
	bound, draining := auditGovernanceDrainGaugesFn(runtime)(ctx)
	if bound != 0 || draining != 1 {
		t.Fatalf("drain gauges bound=%d draining=%d, want 0/1", bound, draining)
	}
	runtime.Close()
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "drain mode") ||
		!strings.Contains(out, "AUDIT_GOVERNANCE_DRAIN") || !strings.Contains(out, "revision=2") {
		t.Fatalf("drain boot log missing WARN/flag/revision: %q", out)
	}
}

// TestBuildAuditGovernanceRuntimeEmptyBindingsRefusesBoot pins R2.1: the
// enabled ∧ ¬drain ∧ empty boot is refused at the seam — the error wraps
// "configure Snaplink Audit Governance" and carries the Validate:194
// "bindings" text through both wraps, no runtime escapes, and the store is
// untouched (no binding row, no control write, no outbox) so the refused
// boot is provably side-effect-free. A.6: the drain WARN stays exclusive to
// the legal drain escape — a refused boot must not emit it (positive pin:
// TestBuildAuditGovernanceRuntimeDrainBootLogsWarn).
func TestBuildAuditGovernanceRuntimeEmptyBindingsRefusesBoot(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "emptyboot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	cfg := &config.Config{AuditGovernance: config.AuditGovernanceConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:1",
		TokenURL: "http://127.0.0.1:1/token",
		HMACKey:  "audit-governance-hmac-key-32-bytes-minimum", Revision: 2,
		HTTPTimeoutSeconds: 1, PollMilliseconds: 10, BatchSize: 10, ClaimTTLSeconds: 3,
		InitialBackoffSeconds: 1, MaxBackoffSeconds: 2, MaxLagSeconds: 4,
		ReconcileBatchSize: 20, DeliveredRetentionSeconds: 3600,
		CleanupIntervalSeconds: 60, CleanupBatchSize: 20,
	}}
	runtime, err := buildAuditGovernanceRuntime(cfg, repo, logger)
	if err == nil || !strings.Contains(err.Error(), "configure Snaplink Audit Governance") ||
		!strings.Contains(err.Error(), "bindings") {
		t.Fatalf("refused boot error=%v, want configured-wrap + bindings text", err)
	}
	if runtime != nil {
		t.Fatalf("refused boot returned runtime %v", runtime)
	}
	store := repo.(auditgovernance.Store)
	safe, err := store.AuditGovernanceCanDisable(ctx)
	if err != nil || !safe {
		t.Fatalf("refused boot mutated governance state: safe=%v err=%v", safe, err)
	}
	// Control-revision probe (same idiom as TestRuntimeNewRejectsEmptyBindings-
	// BeforeStoreIO): CanDisable cannot see a control-row-only write (it
	// checks bindings + undelivered outbox, not the singleton revision), so
	// pin revision == 0 directly — a silent DELETE-all apply before the gate
	// would bump control to cfg.Revision and rollback-reject this probe.
	probe := []repository.AuditGovernanceBindingState{{
		TenantID: "acme", State: repository.AuditGovernanceBindingActive,
	}}
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "probe-digest", probe); err != nil {
		t.Fatalf("control row written before refused boot (revision != 0): %v", err)
	}
	if out := logs.String(); strings.Contains(out, "drain mode") {
		t.Fatalf("refused boot emitted the drain-mode WARN: %q", out)
	}
}
