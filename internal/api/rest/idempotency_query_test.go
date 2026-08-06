package rest

// Acceptance tests for query-string-aware idempotency fingerprints
// (docs/requirements/idempotency-query-fingerprint-v1.md, AC-1..AC-3): a
// DELETE /v1/files/x and DELETE /v1/files/x?hard=1 under the same
// Idempotency-Key must no longer collide — the hard delete must never be
// silently replayed as the soft delete's 204.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/service"
)

// AC-1: fingerprints must distinguish ?hard=1 while identical requests
// (including identical query strings) stay stable — in both fingerprint
// modes (IDEMPOTENCY_HASH_BODY on and off).
func TestIdempotency_FingerprintIncludesQuery(t *testing.T) {
	soft := httptest.NewRequest(http.MethodDelete, "/v1/files/x", nil)
	hard := httptest.NewRequest(http.MethodDelete, "/v1/files/x?hard=1", nil)
	if fingerprint(soft) == fingerprint(hard) {
		t.Fatal("fingerprint must distinguish DELETE /v1/files/x from DELETE /v1/files/x?hard=1")
	}
	if fingerprint(soft) != fingerprint(httptest.NewRequest(http.MethodDelete, "/v1/files/x", nil)) {
		t.Fatal("identical requests must produce identical fingerprints (replay stability)")
	}
	if fingerprint(hard) != fingerprint(httptest.NewRequest(http.MethodDelete, "/v1/files/x?hard=1", nil)) {
		t.Fatal("identical query strings must produce identical fingerprints")
	}
	// bodyFingerprint must also distinguish (IDEMPOTENCY_HASH_BODY=true path).
	const bh = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")
	if bodyFingerprint(soft, bh) == bodyFingerprint(hard, bh) {
		t.Fatal("bodyFingerprint must also distinguish the two URLs")
	}
}

// AC-2: the same key used for soft delete then retried with ?hard=1 must 409
// (IdempotencyConflict), never replay the soft-delete response and never
// re-run the handler; an identical retry must still replay.
func TestIdempotency_QueryVariantConflicts(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent) // 204, same as the Delete handler
	})
	mwh := idempotency(repo, idemSilentLogger(), false)(h)

	do := func(url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, url, nil)
		req.Header.Set("Idempotency-Key", "shared")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do("/v1/files/x")        // soft delete
	rr2 := do("/v1/files/x?hard=1") // hard-delete retry → must 409, not replay
	rr3 := do("/v1/files/x")        // identical retry → must still replay

	if rr1.Code != http.StatusNoContent {
		t.Fatalf("first delete: status=%d want 204", rr1.Code)
	}
	if rr2.Code != http.StatusConflict {
		t.Fatalf("query variant must be 409 Conflict, got %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), "IdempotencyConflict") {
		t.Fatalf("409 must carry code IdempotencyConflict, body=%s", rr2.Body.String())
	}
	if rr2.Header().Get("Idempotency-Replayed") == "true" {
		t.Fatal("query variant must not replay the soft-delete response")
	}
	if calls != 1 {
		t.Fatalf("handler must run for the first request only — replays and conflicts must not re-run it (calls=%d, want 1)", calls)
	}
	if rr3.Code != http.StatusNoContent || rr3.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("identical retry must still replay (status 204, Idempotency-Replayed: true)")
	}
}

// AC-3: end-to-end through setupTest — ?hard=1 with a fresh key must actually
// hard-delete: 204, zero object rows (soft delete would leave a deleted_at
// tombstone) and Get → ErrNotFound.
func TestIdempotency_HardDeleteWithFreshKey(t *testing.T) {
	svc, repo, ts := setupTest(t)
	ctx := context.Background()

	if resp, _ := req(t, "PUT", ts.URL+"/files/x", []byte("data"), nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d want 201", resp.StatusCode)
	}
	resp, _ := req(t, "DELETE", ts.URL+"/files/x?hard=1", nil,
		map[string]string{"Idempotency-Key": "purge-1"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("hard delete: status=%d want 204", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(ctx, "default", "default", "x")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("hard delete must purge all rows, got %d versions (soft delete leaves a tombstone)", len(versions))
	}
	if _, _, err := svc.Get(ctx, "default", "default", "x"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("object must be gone after hard delete, err=%v", err)
	}
}
