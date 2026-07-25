package repository_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openIdemTestRepo(t *testing.T) repository.Repository {
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

// TestIdempotencyClaimAndReplay covers the full claim lifecycle: first claim
// wins, a repeat claim returns the same in-progress record, completing captures
// the response for replay, and deleting releases the key for a fresh claim.
func TestIdempotencyClaimAndReplay(t *testing.T) {
	ctx := context.Background()
	repo := openIdemTestRepo(t)
	const tenant = "default"

	// 1. First claim wins.
	rec, claimed, err := repo.ClaimIdempotencyKey(ctx, tenant, "k1", "fp", "req1")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatalf("first claim: claimed=false, want true")
	}
	if rec.Status != "in_progress" {
		t.Fatalf("first claim status=%q, want in_progress", rec.Status)
	}
	if rec.Fingerprint != "fp" || rec.RequestID != "req1" {
		t.Fatalf("first claim record mismatch: %+v", rec)
	}

	// 2. Second claim of the same (tenant,key) returns the stored record.
	rec2, claimed2, err := repo.ClaimIdempotencyKey(ctx, tenant, "k1", "fp2-ignored", "req2-ignored")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed2 {
		t.Fatalf("second claim: claimed=true, want false")
	}
	if rec2.Status != "in_progress" {
		t.Fatalf("second claim status=%q, want in_progress", rec2.Status)
	}
	if rec2.Fingerprint != "fp" {
		t.Fatalf("second claim fingerprint=%q, want original fp", rec2.Fingerprint)
	}
	if rec2.RequestID != "req1" {
		t.Fatalf("second claim request_id=%q, want original req1", rec2.RequestID)
	}

	// 3. Complete, then a re-claim returns the completed response.
	body := []byte(`{"ok":true}`)
	hdrs := map[string][]string{"Etag": {`"abc123"`}, "Location": {"/v1/files/x"}}
	if err := repo.CompleteIdempotencyKey(ctx, tenant, "k1", 201, body, "application/json", hdrs); err != nil {
		t.Fatalf("complete: %v", err)
	}
	rec3, claimed3, err := repo.ClaimIdempotencyKey(ctx, tenant, "k1", "fp", "req3")
	if err != nil {
		t.Fatalf("claim after complete: %v", err)
	}
	if claimed3 {
		t.Fatalf("claim after complete: claimed=true, want false")
	}
	if rec3.Status != "completed" {
		t.Fatalf("status=%q, want completed", rec3.Status)
	}
	if rec3.ResponseStatus != 201 {
		t.Fatalf("response_status=%d, want 201", rec3.ResponseStatus)
	}
	if !bytes.Equal(rec3.ResponseBody, body) {
		t.Fatalf("response_body=%q, want %q", rec3.ResponseBody, body)
	}
	if rec3.ResponseCT != "application/json" {
		t.Fatalf("response_ct=%q, want application/json", rec3.ResponseCT)
	}
	if got := rec3.ResponseHeaders["Etag"]; len(got) != 1 || got[0] != `"abc123"` {
		t.Fatalf("replayed Etag header=%v, want [\"abc123\"]", got)
	}
	if got := rec3.ResponseHeaders["Location"]; len(got) != 1 || got[0] != "/v1/files/x" {
		t.Fatalf("replayed Location header=%v, want [/v1/files/x]", got)
	}

	// 4. Delete releases the claim; the next claim wins fresh.
	if err := repo.DeleteIdempotencyKey(ctx, tenant, "k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rec4, claimed4, err := repo.ClaimIdempotencyKey(ctx, tenant, "k1", "fp-new", "req4")
	if err != nil {
		t.Fatalf("claim after delete: %v", err)
	}
	if !claimed4 {
		t.Fatalf("claim after delete: claimed=false, want true (fresh claim)")
	}
	if rec4.Status != "in_progress" || rec4.Fingerprint != "fp-new" {
		t.Fatalf("claim after delete record mismatch: %+v", rec4)
	}
}

// TestIdempotencyTenantIsolation verifies the same key under different tenants
// claims independently.
func TestIdempotencyTenantIsolation(t *testing.T) {
	ctx := context.Background()
	repo := openIdemTestRepo(t)

	recA, claimedA, err := repo.ClaimIdempotencyKey(ctx, "a", "shared-key", "fpA", "reqA")
	if err != nil {
		t.Fatalf("tenant a claim: %v", err)
	}
	recB, claimedB, err := repo.ClaimIdempotencyKey(ctx, "b", "shared-key", "fpB", "reqB")
	if err != nil {
		t.Fatalf("tenant b claim: %v", err)
	}
	if !claimedA || !claimedB {
		t.Fatalf("tenant isolation: claimedA=%v claimedB=%v, want both true", claimedA, claimedB)
	}
	if recA.TenantID != "a" || recB.TenantID != "b" {
		t.Fatalf("tenant ids: a=%q b=%q", recA.TenantID, recB.TenantID)
	}
	if recA.Fingerprint != "fpA" || recB.Fingerprint != "fpB" {
		t.Fatalf("fingerprints crossed: a=%q b=%q", recA.Fingerprint, recB.Fingerprint)
	}
}
