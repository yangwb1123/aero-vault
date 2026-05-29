package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestDeleteIdempotencyKeysBefore(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "idemgc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, claimed, err := repo.ClaimIdempotencyKey(ctx, "default", "k1", "fp", "r1"); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}

	// Cutoff in the past → nothing removed (the key was created just now).
	if n, err := repo.DeleteIdempotencyKeysBefore(ctx, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil || n != 0 {
		t.Fatalf("past cutoff: n=%d err=%v (want 0)", n, err)
	}

	// Cutoff in the future → the key is purged.
	if n, err := repo.DeleteIdempotencyKeysBefore(ctx, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil || n != 1 {
		t.Fatalf("future cutoff: n=%d err=%v (want 1)", n, err)
	}

	// After purge the key is free to claim afresh.
	if _, claimed, err := repo.ClaimIdempotencyKey(ctx, "default", "k1", "fp", "r2"); err != nil || !claimed {
		t.Fatalf("re-claim after purge: claimed=%v err=%v", claimed, err)
	}
}
