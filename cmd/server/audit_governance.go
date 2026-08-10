package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/billing"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func buildAuditGovernanceRuntime(
	cfg *config.Config, repo repository.Repository, logger *slog.Logger,
) (*auditgovernance.Runtime, error) {
	if !cfg.AuditGovernance.Enabled {
		store, ok := repo.(auditgovernance.Store)
		if !ok {
			return nil, fmt.Errorf("repository does not support Audit Governance persistence")
		}
		timeout := time.Duration(cfg.AuditGovernance.HTTPTimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		safe, err := store.AuditGovernanceCanDisable(ctx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("verify disabled Audit Governance state")
		}
		if !safe {
			return nil, fmt.Errorf("Audit Governance cannot be disabled before bindings are drained and removed")
		}
		return nil, nil
	}
	store, ok := repo.(auditgovernance.Store)
	if !ok {
		return nil, fmt.Errorf("repository does not support Audit Governance persistence")
	}
	runtime, err := auditgovernance.New(cfg.AuditGovernance, store, logger)
	if err != nil {
		return nil, fmt.Errorf("configure Snaplink Audit Governance: %w", err)
	}
	if runtime.Draining() {
		// Drain mode is the only legal zero-tenant enabled boot (the empty-
		// manifest gate is bypassed by AUDIT_GOVERNANCE_DRAIN). It is a
		// transition state, never silent: a distinct WARN (healthy boots keep
		// the INFO line byte-identical) naming the flag + revision + digest
		// fingerprint — the only trace that survives the forensically
		// destructive DELETE-all beside the control-row revision watermark.
		logger.Warn("Snaplink Audit Governance relay enabled — drain mode",
			"flag", "AUDIT_GOVERNANCE_DRAIN", "tenants", len(cfg.AuditGovernance.Bindings),
			"revision", cfg.AuditGovernance.Revision,
			"digest", digestFingerprint(runtime.AppliedDigest()))
		return runtime, nil
	}
	logger.Info("Snaplink Audit Governance relay enabled",
		"tenants", len(cfg.AuditGovernance.Bindings), "revision", cfg.AuditGovernance.Revision)
	return runtime, nil
}

// digestFingerprint shortens the persisted desired-manifest digest
// (43-char base64url) to a log-safe prefix for the drain-mode WARN line.
func digestFingerprint(digest string) string {
	if len(digest) <= 8 {
		return digest
	}
	return digest[:8]
}

func runtimeReadiness(
	billingRuntime *billing.Runtime, auditRuntime *auditgovernance.Runtime,
) readinessChecker {
	var checks readinessGroup
	if billingRuntime != nil {
		checks = append(checks, billingRuntime)
	}
	if auditRuntime != nil {
		checks = append(checks, auditRuntime)
	}
	if len(checks) == 0 {
		return nil
	}
	return checks
}
