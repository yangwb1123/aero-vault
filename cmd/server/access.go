package main

import (
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func buildAccessManager(cfg *config.Config, repo repository.Repository) (*access.Manager, error) {
	if !cfg.Access.Enabled {
		return nil, nil
	}
	store, ok := repo.(access.Store)
	if !ok {
		return nil, fmt.Errorf("repository does not implement enterprise access store")
	}
	return access.NewManager(store, access.Config{
		Enabled:        true,
		DefaultPolicy:  cfg.Access.DefaultPolicy,
		ShareSecret:    []byte(cfg.Access.ShareSecret),
		DeleteFailOpen: !cfg.Access.DeleteFailClosed,
	})
}
