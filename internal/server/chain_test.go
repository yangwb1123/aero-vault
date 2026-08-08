package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// wantRings pins the 12-ring registration order (outermost = last). It is the
// AC-1 shape contract: deleting or reordering any ring fails this test, so the
// "remove a chain line in main.go and the suite stays green" gap is closed by
// construction.
var wantRings = []string{
	RingAccessLog,
	RingConcurrency,
	RingRecoverer,
	RingOTel,
	RingRateLimit,
	RingTenant,
	RingAuth,
	RingMaxBody,
	RingSecureHeaders,
	RingCORS,
	RingBucketCORS,
	RingRequestID,
}

func newTestChain(t *testing.T) (repository.Repository, *auth.Registry, *config.Config, *slog.Logger) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	authReg, err := auth.Parse("")
	if err != nil {
		t.Fatalf("auth.Parse: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return repo, authReg, &config.Config{}, logger
}

func ringNames(links []ChainLink) []string {
	names := make([]string, len(links))
	for i, l := range links {
		names[i] = l.Name
	}
	return names
}

// TestBuildChain_12RingsInOrder is AC-1: exactly 12 rings, in the pinned
// production order, every middleware non-nil, and idempotent across calls.
func TestBuildChain_12RingsInOrder(t *testing.T) {
	repo, authReg, cfg, logger := newTestChain(t)
	concurrencyMW := middleware.NewConcurrencyLimiter(0).Middleware()

	links := BuildChain(repo, authReg, nil, cfg, logger, concurrencyMW, nil)
	if len(links) != len(wantRings) {
		t.Fatalf("chain has %d rings, want %d (%v)", len(links), len(wantRings), ringNames(links))
	}
	got := ringNames(links)
	for i, want := range wantRings {
		if got[i] != want {
			t.Fatalf("ring %d = %q, want %q (order drift: %v)", i, got[i], want, got)
		}
	}
	for _, l := range links {
		if l.MW == nil {
			t.Fatalf("ring %q has nil MW", l.Name)
		}
	}
}

// TestBuildChain_Idempotent pins that repeated calls return equal chains and
// fresh slices (no shared mutable state between calls).
func TestBuildChain_Idempotent(t *testing.T) {
	repo, authReg, cfg, logger := newTestChain(t)
	concurrencyMW := middleware.NewConcurrencyLimiter(0).Middleware()

	first := BuildChain(repo, authReg, nil, cfg, logger, concurrencyMW, nil)
	second := BuildChain(repo, authReg, nil, cfg, logger, concurrencyMW, nil)
	firstNames, secondNames := ringNames(first), ringNames(second)
	for i := range wantRings {
		if firstNames[i] != secondNames[i] {
			t.Fatalf("call 2 ring %d = %q, want %q", i, secondNames[i], firstNames[i])
		}
	}
	if &first[0] == &second[0] {
		t.Fatal("BuildChain returned a shared backing slice across calls")
	}
}

// TestBuildChain_NilConcurrencyMWPanics is the A1 fail-fast fold: a nil
// concurrency middleware is an assembly bug (it would silently shrink the
// chain to 11 rings), so it must panic at construction, not degrade.
func TestBuildChain_NilConcurrencyMWPanics(t *testing.T) {
	repo, authReg, cfg, logger := newTestChain(t)
	defer func() {
		if recover() == nil {
			t.Fatal("BuildChain with nil concurrencyMW did not panic")
		}
	}()
	BuildChain(repo, authReg, nil, cfg, logger, nil, nil)
}

// TestApplyMiddleware_PassesThrough ensures ApplyMiddleware wires the chain
// without rejecting plain requests (the zero-config harness shape: no auth,
// unlimited body, empty CORS, disabled concurrency).
func TestApplyMiddleware_PassesThrough(t *testing.T) {
	repo, authReg, cfg, logger := newTestChain(t)
	handler := ApplyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), repo, authReg, nil, cfg, logger, middleware.NewConcurrencyLimiter(0).Middleware(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("secure headers ring not applied: X-Content-Type-Options = %q", rec.Header().Get("X-Content-Type-Options"))
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("request_id ring not applied: X-Request-ID empty")
	}
}
