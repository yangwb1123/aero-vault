package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// ── Bucket Operations ────────────────────────────────────────────────────

func TestBucketExists(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	const tenant, bucket = "default", "exists-test"

	// Non-existent bucket.
	ok, err := repo.BucketExists(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("BucketExists: %v", err)
	}
	if ok {
		t.Fatal("expected false for non-existent bucket")
	}

	// Create and verify.
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	ok, err = repo.BucketExists(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("BucketExists after create: %v", err)
	}
	if !ok {
		t.Fatal("expected true after create")
	}

	// Different tenant isolation.
	isolated, err := repo.BucketExists(ctx, "other-tenant", bucket)
	if err != nil {
		t.Fatalf("BucketExists other tenant: %v", err)
	}
	if isolated {
		t.Fatal("expected false for different tenant")
	}
}

func TestBucketStats(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	const tenant, bucket = "default", "stats-test"

	// Ensure bucket exists.
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	// Empty bucket stats.
	stats, err := repo.BucketStats(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("BucketStats: %v", err)
	}
	if stats.ObjectCount != 0 || stats.TotalSize != 0 {
		t.Fatalf("expected empty stats, got objects=%d size=%d", stats.ObjectCount, stats.TotalSize)
	}

	// Add an object.
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "doc.txt",
		Backend: "local", StorageKey: "sk1", Size: 100, ETag: "e1",
	}); err != nil {
		t.Fatalf("UpsertObject: %v", err)
	}

	stats, err = repo.BucketStats(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("BucketStats after upsert: %v", err)
	}
	if stats.ObjectCount != 1 || stats.TotalSize != 100 {
		t.Fatalf("expected 1 object 100 bytes, got objects=%d size=%d", stats.ObjectCount, stats.TotalSize)
	}
}

func TestDeleteBucket(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	const tenant, bucket = "default", "delete-test"

	// Delete non-existent returns ErrNotFound.
	if err := repo.DeleteBucket(ctx, tenant, bucket); err == nil {
		t.Fatal("expected ErrNotFound")
	} else if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}

	// Create, add object, then delete.
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "doc.txt",
		Backend: "local", StorageKey: "sk1", Size: 100, ETag: "e1",
	}); err != nil {
		t.Fatalf("UpsertObject: %v", err)
	}

	if err := repo.DeleteBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}

	// Bucket should be gone.
	ok, err := repo.BucketExists(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("BucketExists after delete: %v", err)
	}
	if ok {
		t.Fatal("expected bucket to be deleted")
	}
}

// ── Bucket CORS ───────────────────────────────────────────────────────────

func TestBucketCORS_CRUD(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	const tenant, bucket = "default", "cors-test"

	// Initially no rules.
	rules, err := repo.GetBucketCORS(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketCORS: %v", err)
	}
	if rules == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(rules))
	}

	// Set rules.
	setRules := []repository.CORSRule{
		{AllowedOrigins: []string{"https://example.com"}, AllowedMethods: []string{"GET"}, MaxAgeSeconds: 3600},
	}
	if err := repo.SetBucketCORS(ctx, tenant, bucket, setRules); err != nil {
		t.Fatalf("SetBucketCORS: %v", err)
	}

	// Read back.
	rules, err = repo.GetBucketCORS(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketCORS after set: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].AllowedOrigins[0] != "https://example.com" {
		t.Errorf("origin: got %q", rules[0].AllowedOrigins[0])
	}

	// Delete.
	if err := repo.DeleteBucketCORS(ctx, tenant, bucket); err != nil {
		t.Fatalf("DeleteBucketCORS: %v", err)
	}
	rules, err = repo.GetBucketCORS(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketCORS after delete: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules after delete, got %d", len(rules))
	}
}

// ── Bucket Logging ────────────────────────────────────────────────────────

func TestBucketLogging_CRUD(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	const tenant, bucket = "default", "log-test"

	// Initially disabled.
	cfg, err := repo.GetBucketLogging(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketLogging: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("expected disabled initially")
	}

	// Enable.
	target := "log-target"
	prefix := "logs/"
	if err := repo.SetBucketLogging(ctx, tenant, bucket, target, prefix); err != nil {
		t.Fatalf("SetBucketLogging: %v", err)
	}

	// Read back.
	cfg, err = repo.GetBucketLogging(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketLogging after set: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("expected enabled after set")
	}
	if cfg.Target != target {
		t.Errorf("target: got %q, want %q", cfg.Target, target)
	}
	if cfg.Prefix != prefix {
		t.Errorf("prefix: got %q, want %q", cfg.Prefix, prefix)
	}

	// Disable.
	if err := repo.DeleteBucketLogging(ctx, tenant, bucket); err != nil {
		t.Fatalf("DeleteBucketLogging: %v", err)
	}
	cfg, err = repo.GetBucketLogging(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketLogging after delete: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("expected disabled after delete")
	}
}

// ── Bucket Notifications ──────────────────────────────────────────────────

func TestBucketNotifications_CRUD(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	const tenant, bucket = "default", "notif-test"

	// Initially no rules.
	rules, err := repo.GetBucketNotifications(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketNotifications: %v", err)
	}
	if rules != nil {
		t.Fatal("expected nil initially")
	}

	// Set rules.
	setRules := []repository.NotificationRule{
		{ID: "rule1", Events: []string{"s3:ObjectCreated:*"}, QueueARN: "arn:aws:sqs:us-east-1:123:queue"},
	}
	if err := repo.SetBucketNotifications(ctx, tenant, bucket, setRules); err != nil {
		t.Fatalf("SetBucketNotifications: %v", err)
	}

	// Read back.
	rules, err = repo.GetBucketNotifications(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketNotifications after set: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ID != "rule1" {
		t.Errorf("rule ID: got %q", rules[0].ID)
	}

	// Delete.
	if err := repo.DeleteBucketNotifications(ctx, tenant, bucket); err != nil {
		t.Fatalf("DeleteBucketNotifications: %v", err)
	}
	rules, err = repo.GetBucketNotifications(ctx, tenant, bucket)
	if err != nil {
		t.Fatalf("GetBucketNotifications after delete: %v", err)
	}
	if rules != nil {
		t.Fatal("expected nil after delete")
	}
}

func openTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

// TestListBuckets verifies that ListBuckets returns the buckets created for a
// tenant and never leaks buckets belonging to another tenant.
func TestListBuckets(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	if err := repo.CreateBucket(ctx, "tenantA", "alpha"); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if err := repo.CreateBucket(ctx, "tenantA", "beta"); err != nil {
		t.Fatalf("create beta: %v", err)
	}
	if err := repo.CreateBucket(ctx, "tenantB", "gamma"); err != nil {
		t.Fatalf("create gamma: %v", err)
	}

	got, err := repo.ListBuckets(ctx, "tenantA")
	if err != nil {
		t.Fatalf("list tenantA: %v", err)
	}
	sort.Strings(got)
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("tenantA buckets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tenantA buckets = %v, want %v", got, want)
		}
	}

	// A tenant with no buckets gets an empty (non-nil) slice.
	none, err := repo.ListBuckets(ctx, "tenantC")
	if err != nil {
		t.Fatalf("list tenantC: %v", err)
	}
	if none == nil {
		t.Fatalf("expected non-nil empty slice for tenant with no buckets")
	}
	if len(none) != 0 {
		t.Fatalf("expected no buckets for tenantC, got %v", none)
	}
}

// TestStorageKeyReferenced verifies that a storage key is reported as
// referenced while a row exists, including after the object is soft-deleted,
// and is not reported for an unknown key.
func TestStorageKeyReferenced(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	const tenant, bucket, key, storageKey = "default", "default", "doc.txt", "blob/abc123"

	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID:   tenant,
		Bucket:     bucket,
		Key:        key,
		StorageKey: storageKey,
		Backend:    "local",
		Size:       5,
		ETag:       "e1",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ref, err := repo.StorageKeyReferenced(ctx, storageKey)
	if err != nil {
		t.Fatalf("referenced: %v", err)
	}
	if !ref {
		t.Fatalf("expected storage key %q to be referenced", storageKey)
	}

	unknown, err := repo.StorageKeyReferenced(ctx, "blob/does-not-exist")
	if err != nil {
		t.Fatalf("referenced unknown: %v", err)
	}
	if unknown {
		t.Fatalf("expected unknown storage key to be unreferenced")
	}

	// Soft-deleted objects still pin their blob.
	if err := repo.SoftDeleteObject(ctx, tenant, bucket, key); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	stillRef, err := repo.StorageKeyReferenced(ctx, storageKey)
	if err != nil {
		t.Fatalf("referenced after delete: %v", err)
	}
	if !stillRef {
		t.Fatalf("expected storage key %q to remain referenced after soft delete", storageKey)
	}
}
