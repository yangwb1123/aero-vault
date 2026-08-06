package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

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
