package main

import (
	"fmt"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/billing"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func buildBillingRuntime(
	cfg *config.Config, repo repository.Repository, logger *slog.Logger,
) (*billing.Runtime, error) {
	if !cfg.Billing.Enabled {
		return nil, nil
	}
	store, ok := repo.(billing.Store)
	if !ok {
		return nil, fmt.Errorf("repository does not support Snaplink billing persistence")
	}
	runtime, err := billing.New(cfg.Billing, store, logger)
	if err != nil {
		return nil, fmt.Errorf("configure Snaplink billing: %w", err)
	}
	logger.Info("Snaplink billing enabled", "tenants", len(cfg.Billing.Bindings))
	return runtime, nil
}

func wrapBillingRepository(
	repo repository.Repository, runtime *billing.Runtime,
) repository.Repository {
	return billing.WrapRepository(repo, runtime)
}
