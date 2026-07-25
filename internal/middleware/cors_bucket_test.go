package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func newTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "cors.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

func TestBucketCORS_AllowsConfiguredOrigin(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	// /v1/files/... maps to bucket "default". Set CORS on the default bucket.
	if err := repo.SetBucketCORS(ctx, "default", "default", []repository.CORSRule{
		{AllowedOrigins: []string{"https://app.example.com"}, AllowedMethods: []string{"GET"}},
	}); err != nil {
		t.Fatalf("SetBucketCORS: %v", err)
	}

	provider := NewBucketCORSProvider(repo, time.Second)
	defer provider.Close()

	nextCalled := false
	h := BucketCORS(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	req := httptest.NewRequest("GET", "/v1/files/doc.txt", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatal("allowed origin should not be forbidden")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Errorf("expected Access-Control-Allow-Origin header, got none")
	}
	if !nextCalled {
		t.Fatal("next handler should have been called")
	}
}

func TestBucketCORS_NoBlockWithoutRules(t *testing.T) {
	repo := newTestRepo(t)

	provider := NewBucketCORSProvider(repo, time.Second)
	defer provider.Close()

	nextCalled := false
	h := BucketCORS(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	// No CORS rules set; request should pass through.
	req := httptest.NewRequest("GET", "/v1/files/doc.txt", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatal("request without CORS rules should not be blocked")
	}
	if !nextCalled {
		t.Fatal("next handler should have been called")
	}
}

func TestBucketCORSProvider_CacheInvalidate(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	provider := NewBucketCORSProvider(repo, time.Hour)
	defer provider.Close()

	// Should return empty (no rules set).
	rules, err := provider.GetCORSRules(ctx, "default", "no-such-bucket")
	if err != nil {
		t.Fatalf("GetCORSRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(rules))
	}

	// Set rules and invalidate cache.
	if err := repo.SetBucketCORS(ctx, "default", "no-such-bucket", []repository.CORSRule{
		{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET"}},
	}); err != nil {
		t.Fatalf("SetBucketCORS: %v", err)
	}
	provider.InvalidateBucket(ctx, "default", "no-such-bucket")

	// Should now see the new rules.
	rules, err = provider.GetCORSRules(ctx, "default", "no-such-bucket")
	if err != nil {
		t.Fatalf("GetCORSRules after invalidate: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule after invalidate, got %d", len(rules))
	}
}
