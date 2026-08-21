package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/billing"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestBillingReadyzBacklogLagReturnsDegraded200(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "readyz-billing.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		_ = repo.Close()
		t.Fatal(err)
	}
	store := repo.(billing.Store)
	now := time.Now().UTC()
	if _, err := store.ApplyBillingProjection(ctx, repository.BillingProjection{
		TenantID: "acme", Revision: 1, Active: true,
		Bytes: repository.BillingLimit{Hard: 100}, Objects: repository.BillingLimit{Hard: 10},
		EffectiveAt: now.Add(-time.Minute), ProjectedAt: now,
	}); err != nil {
		_ = repo.Close()
		t.Fatal(err)
	}
	if _, _, err := store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
		OperationID: "readyz-billing-1", TenantID: "acme", Kind: "object_write",
		DeltaBytes: 1, OccurredAt: now,
	}); err != nil {
		_ = repo.Close()
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = repo.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE billing_usage_outbox SET created_at_ns=? WHERE tenant_id=?`,
		time.Now().UTC().Add(-5*time.Second).UnixNano(), "acme")
	_ = db.Close()
	if err != nil {
		_ = repo.Close()
		t.Fatal(err)
	}
	runtime, err := billing.New(config.BillingConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:1", TokenURL: "http://127.0.0.1:1/token",
		AllowInsecureHTTP: true, HTTPTimeoutSeconds: 1, ProjectionIntervalSec: 60,
		OutboxPollMillis: 100, OutboxBatchSize: 8, ClaimTTLSeconds: 2,
		OutboxMaxAttempts: 3, MaxLagSeconds: 3,
		Bindings: []config.BillingBinding{{TenantID: "acme", ClientID: "client", ClientSecret: "secret"}},
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_ = repo.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		runtime.Close()
		_ = repo.Close()
	})
	code, body, elapsed := serveReadyz(t, runtimeReadiness(runtime, nil))
	if code != 200 {
		t.Fatalf("status=%d body=%q, want degraded 200", code, body)
	}
	if !strings.HasPrefix(body, `{"ok":true,"degraded":true,"backlog_age_seconds":`) {
		t.Fatalf("body=%q missing billing degraded payload", body)
	}
	if elapsed >= time.Second {
		t.Fatalf("readyz elapsed=%v, want bounded healthy probe", elapsed)
	}
}
