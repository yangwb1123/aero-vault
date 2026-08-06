package billing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func openRuntimeTestStore(t *testing.T) (repository.Repository, Store) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, ok := repo.(Store)
	if !ok {
		t.Fatal("repository does not implement billing Store")
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, store
}

func TestRuntimeFailsUnknownProjectionClosed(t *testing.T) {
	_, store := openRuntimeTestStore(t)
	runtime := &Runtime{
		store: store, bindings: map[string]*Client{"acme": nil},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	err := runtime.CheckQuota(context.Background(), "acme",
		repository.TenantQuota{TenantID: "acme"}, 1, 1)
	if !errors.Is(err, service.ErrEntitlementUnavailable) {
		t.Fatalf("missing projection error=%v", err)
	}
	if err := runtime.Ready(context.Background()); !errors.Is(err, service.ErrEntitlementUnavailable) {
		t.Fatalf("readiness error=%v", err)
	}
}

func TestRuntimeEnforcesExplicitZeroAndPreservesProjectedUse(t *testing.T) {
	ctx := context.Background()
	repo, store := openRuntimeTestStore(t)
	now := time.Now().UTC()
	_, err := store.ApplyBillingProjection(ctx, repository.BillingProjection{
		TenantID: "acme", Revision: 1, Active: true,
		Bytes:       repository.BillingLimit{Hard: 0},
		Objects:     repository.BillingLimit{Hard: 10},
		EffectiveAt: now.Add(-time.Minute), ProjectedAt: now,
	})
	if err != nil {
		t.Fatalf("apply projection: %v", err)
	}
	runtime := &Runtime{store: store, bindings: map[string]*Client{"acme": nil}}
	quota, err := repo.GetTenantQuota(ctx, "acme")
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if err := runtime.CheckQuota(ctx, "acme", quota, 1, 0); !errors.Is(err, service.ErrQuotaExceeded) {
		t.Fatalf("explicit hard zero was not enforced: %v", err)
	}
	if err := runtime.CheckQuota(ctx, "acme", quota, 0, 0); err != nil {
		t.Fatalf("non-growing mutation should be allowed: %v", err)
	}
}
