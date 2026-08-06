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
	logger.Info("Snaplink Audit Governance relay enabled",
		"tenants", len(cfg.AuditGovernance.Bindings), "revision", cfg.AuditGovernance.Revision)
	return runtime, nil
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
