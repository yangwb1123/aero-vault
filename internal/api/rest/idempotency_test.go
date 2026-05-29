package rest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func idemTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(t.TempDir(), "idem.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

func idemSilentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestIdempotency_ReplaysOnRetry: a second write with the same key replays the
// original response and does not re-run the handler.
func TestIdempotency_ReplaysOnRetry(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"version":%d}`, calls)
	})
	mwh := idempotency(repo, idemSilentLogger())(h)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("body"))
		req.Header.Set("Idempotency-Key", "key-1")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do()
	rr2 := do()

	if calls != 1 {
		t.Fatalf("handler should run exactly once, ran %d", calls)
	}
	if rr1.Code != rr2.Code {
		t.Fatalf("replay status mismatch: %d vs %d", rr1.Code, rr2.Code)
	}
	if rr1.Body.String() != rr2.Body.String() {
		t.Fatalf("replay body mismatch: %q vs %q", rr1.Body.String(), rr2.Body.String())
	}
	if rr1.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("first response must not be marked replayed")
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("replayed response must carry Idempotency-Replayed: true")
	}
	if rr2.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay must restore Content-Type, got %q", rr2.Header().Get("Content-Type"))
	}
}

// TestIdempotency_NoHeaderPassThrough: without the header the middleware is
// inert and the handler runs every time.
func TestIdempotency_NoHeaderPassThrough(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger())(h)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("body"))
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
	}
	if calls != 2 {
		t.Fatalf("no header → handler should run each time, ran %d", calls)
	}
}

// TestIdempotency_DifferentRequestConflict: reusing a key for a different
// path is rejected with 409.
func TestIdempotency_DifferentRequestConflict(t *testing.T) {
	repo := idemTestRepo(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger())(h)

	req1 := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("x"))
	req1.Header.Set("Idempotency-Key", "shared")
	rr1 := httptest.NewRecorder()
	mwh.ServeHTTP(rr1, req1)

	req2 := httptest.NewRequest(http.MethodPut, "/v1/files/b.txt", strings.NewReader("x"))
	req2.Header.Set("Idempotency-Key", "shared")
	rr2 := httptest.NewRecorder()
	mwh.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for key reused on a different path, got %d", rr2.Code)
	}
}

// TestIdempotency_5xxReleasesClaim: a 5xx response is not memoized; the claim
// is released so a retry re-executes the handler.
func TestIdempotency_5xxReleasesClaim(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "boom")
			return
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "ok")
	})
	mwh := idempotency(repo, idemSilentLogger())(h)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("body"))
		req.Header.Set("Idempotency-Key", "key-5xx")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do()
	rr2 := do()

	if rr1.Code != http.StatusInternalServerError {
		t.Fatalf("first call should be 500, got %d", rr1.Code)
	}
	if calls != 2 {
		t.Fatalf("5xx must release the claim so retry re-runs; calls=%d", calls)
	}
	if rr2.Code != http.StatusCreated {
		t.Fatalf("retry after 5xx should re-execute and return 201, got %d", rr2.Code)
	}
	if rr2.Header().Get("Idempotency-Replayed") == "true" {
		t.Fatal("a released claim must not replay")
	}
}

// TestIdempotency_InProgressConflict: a key already claimed (in progress)
// rejects a concurrent duplicate with 409 and never runs the handler.
func TestIdempotency_InProgressConflict(t *testing.T) {
	repo := idemTestRepo(t)

	// Pre-claim the key with the exact fingerprint the request will produce.
	probe := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", nil)
	fp := fingerprint(probe)
	_, claimed, err := repo.ClaimIdempotencyKey(context.Background(), "default", "k-inflight", fp, "req0")
	if err != nil || !claimed {
		t.Fatalf("setup claim failed: claimed=%v err=%v", claimed, err)
	}

	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger())(h)

	req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", nil)
	req.Header.Set("Idempotency-Key", "k-inflight")
	rr := httptest.NewRecorder()
	mwh.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for in-progress key, got %d", rr.Code)
	}
	if calls != 0 {
		t.Fatalf("handler must not run while a claim is in progress; calls=%d", calls)
	}
}
