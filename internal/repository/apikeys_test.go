package repository_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openAPIKeyTestRepo(t *testing.T) repository.Repository {
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

// TestAPIKeyPutGetRoundTrip stores a key then reads it back, checking every
// field survives the round trip and that an unknown hash reports not-found.
func TestAPIKeyPutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := openAPIKeyTestRepo(t)

	rec := repository.APIKeyRecord{
		TokenHash:  "hash-1",
		TenantID:   "acme",
		Scopes:     "read+write",
		Label:      "ci token",
		CreatedAt:  "2026-01-01T00:00:00Z",
		ExpiresAt:  "2026-12-31T00:00:00Z",
		LastUsedAt: "",
	}
	if err := repo.PutAPIKey(ctx, rec); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, found, err := repo.GetAPIKeyByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatalf("get: found=false, want true")
	}
	if got != rec {
		t.Fatalf("round trip mismatch:\n got=%+v\nwant=%+v", got, rec)
	}

	// 2. Unknown hash → not found, no error.
	_, found, err = repo.GetAPIKeyByHash(ctx, "nope")
	if err != nil {
		t.Fatalf("get unknown: %v", err)
	}
	if found {
		t.Fatalf("get unknown: found=true, want false")
	}
}

// TestAPIKeyUpsertKeepsCreatedAt re-puts under the same hash with changed
// scopes/label and verifies created_at is preserved while the mutable fields
// update.
func TestAPIKeyUpsertKeepsCreatedAt(t *testing.T) {
	ctx := context.Background()
	repo := openAPIKeyTestRepo(t)

	orig := repository.APIKeyRecord{
		TokenHash: "hash-2",
		TenantID:  "acme",
		Scopes:    "read",
		Label:     "old",
		CreatedAt: "2026-01-01T00:00:00Z",
		ExpiresAt: "",
	}
	if err := repo.PutAPIKey(ctx, orig); err != nil {
		t.Fatalf("put orig: %v", err)
	}

	updated := orig
	updated.Scopes = "read+write"
	updated.Label = "new"
	updated.CreatedAt = "2099-09-09T09:09:09Z" // should be ignored on conflict
	if err := repo.PutAPIKey(ctx, updated); err != nil {
		t.Fatalf("put updated: %v", err)
	}

	got, found, err := repo.GetAPIKeyByHash(ctx, "hash-2")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.Scopes != "read+write" {
		t.Fatalf("scopes=%q, want read+write", got.Scopes)
	}
	if got.Label != "new" {
		t.Fatalf("label=%q, want new", got.Label)
	}
	if got.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("created_at=%q, want original preserved", got.CreatedAt)
	}
}

// TestAPIKeyList covers tenant filtering and the all-tenants ("") variant.
func TestAPIKeyList(t *testing.T) {
	ctx := context.Background()
	repo := openAPIKeyTestRepo(t)

	keys := []repository.APIKeyRecord{
		{TokenHash: "a1", TenantID: "acme", CreatedAt: "2026-01-01T00:00:01Z"},
		{TokenHash: "a2", TenantID: "acme", CreatedAt: "2026-01-01T00:00:02Z"},
		{TokenHash: "b1", TenantID: "beta", CreatedAt: "2026-01-01T00:00:03Z"},
	}
	for _, k := range keys {
		if err := repo.PutAPIKey(ctx, k); err != nil {
			t.Fatalf("put %s: %v", k.TokenHash, err)
		}
	}

	acme, err := repo.ListAPIKeys(ctx, "acme")
	if err != nil {
		t.Fatalf("list acme: %v", err)
	}
	if len(acme) != 2 {
		t.Fatalf("list acme: got %d, want 2", len(acme))
	}
	for _, k := range acme {
		if k.TenantID != "acme" {
			t.Fatalf("list acme returned tenant %q", k.TenantID)
		}
	}

	all, err := repo.ListAPIKeys(ctx, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list all: got %d, want 3", len(all))
	}

	// Empty result is non-nil.
	none, err := repo.ListAPIKeys(ctx, "ghost")
	if err != nil {
		t.Fatalf("list ghost: %v", err)
	}
	if none == nil {
		t.Fatalf("list ghost: got nil slice, want non-nil empty")
	}
	if len(none) != 0 {
		t.Fatalf("list ghost: got %d, want 0", len(none))
	}
}

// TestAPIKeyDelete verifies delete reports true once, then false, and that the
// row is actually gone.
func TestAPIKeyDelete(t *testing.T) {
	ctx := context.Background()
	repo := openAPIKeyTestRepo(t)

	if err := repo.PutAPIKey(ctx, repository.APIKeyRecord{TokenHash: "d1", TenantID: "acme", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("put: %v", err)
	}

	deleted, err := repo.DeleteAPIKeyByHash(ctx, "d1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatalf("delete: got false, want true")
	}

	_, found, err := repo.GetAPIKeyByHash(ctx, "d1")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if found {
		t.Fatalf("get after delete: found=true, want false")
	}

	deleted, err = repo.DeleteAPIKeyByHash(ctx, "d1")
	if err != nil {
		t.Fatalf("delete again: %v", err)
	}
	if deleted {
		t.Fatalf("delete again: got true, want false")
	}
}

// TestAPIKeyTouch verifies TouchAPIKey updates last_used_at.
func TestAPIKeyTouch(t *testing.T) {
	ctx := context.Background()
	repo := openAPIKeyTestRepo(t)

	if err := repo.PutAPIKey(ctx, repository.APIKeyRecord{TokenHash: "t1", TenantID: "acme", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("put: %v", err)
	}

	const when = "2026-05-29T12:00:00Z"
	if err := repo.TouchAPIKey(ctx, "t1", when); err != nil {
		t.Fatalf("touch: %v", err)
	}

	got, found, err := repo.GetAPIKeyByHash(ctx, "t1")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.LastUsedAt != when {
		t.Fatalf("last_used_at=%q, want %q", got.LastUsedAt, when)
	}
}
