package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openLeaseTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// TestAcquireLeaseLifecycle covers creation, self-renewal, contention while a
// lease is valid, and takeover once the lease has expired.
func TestAcquireLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := openLeaseTestRepo(t)

	// 1. First acquire creates the row.
	ok, err := repo.AcquireLease(ctx, "job", "node-A", time.Minute)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !ok {
		t.Fatalf("first acquire: ok=false, want true")
	}

	// 2. node-A renews its own lease.
	ok, err = repo.AcquireLease(ctx, "job", "node-A", time.Minute)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !ok {
		t.Fatalf("renew: ok=false, want true")
	}

	// 3. node-B cannot take over while A's lease is valid.
	ok, err = repo.AcquireLease(ctx, "job", "node-B", time.Minute)
	if err != nil {
		t.Fatalf("contended acquire: %v", err)
	}
	if ok {
		t.Fatalf("contended acquire: ok=true, want false (A still holds it)")
	}

	// 4. Force A's lease to be already expired with a negative ttl, then B takes over.
	ok, err = repo.AcquireLease(ctx, "job", "node-A", -time.Second)
	if err != nil {
		t.Fatalf("expire self: %v", err)
	}
	if !ok {
		t.Fatalf("expire self: ok=false, want true (A renews into the past)")
	}
	ok, err = repo.AcquireLease(ctx, "job", "node-B", time.Minute)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if !ok {
		t.Fatalf("takeover: ok=false, want true (B takes over expired lease)")
	}
	// A can no longer reclaim while B's fresh lease is valid.
	ok, err = repo.AcquireLease(ctx, "job", "node-A", time.Minute)
	if err != nil {
		t.Fatalf("post-takeover acquire: %v", err)
	}
	if ok {
		t.Fatalf("post-takeover acquire: ok=true, want false (B now holds it)")
	}
}

// TestAcquireLeaseIndependentNames verifies leases keyed by different names do
// not interfere with each other.
func TestAcquireLeaseIndependentNames(t *testing.T) {
	ctx := context.Background()
	repo := openLeaseTestRepo(t)

	ok, err := repo.AcquireLease(ctx, "job-1", "node-A", time.Minute)
	if err != nil {
		t.Fatalf("job-1 acquire: %v", err)
	}
	if !ok {
		t.Fatalf("job-1 acquire: ok=false, want true")
	}

	// A different name is independent: node-B holds job-2 even though A holds job-1.
	ok, err = repo.AcquireLease(ctx, "job-2", "node-B", time.Minute)
	if err != nil {
		t.Fatalf("job-2 acquire: %v", err)
	}
	if !ok {
		t.Fatalf("job-2 acquire: ok=false, want true (independent lease name)")
	}
}
