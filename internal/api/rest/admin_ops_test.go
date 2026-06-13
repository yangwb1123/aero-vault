package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// TestAdminUsage verifies that GET /v1/usage returns a 200 with the expected
// quota fields. With no auth/tenant middleware the tenant defaults to "default".
func TestAdminUsage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "ops.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("new local storage: %v", err)
	}
	svc := service.NewFileService(store, repo, nil)
	reg, _ := auth.Parse("")
	adm := NewAdminHandler(svc, repo, reg)

	r := chi.NewRouter()
	r.Get("/v1/usage", adm.Usage)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	resp, body := req(t, "GET", srv.URL+"/v1/usage", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("usage: status=%d body=%s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("usage decode: %v", err)
	}
	for _, key := range []string{"tenant", "used_bytes", "used_objects", "max_bytes", "max_objects"} {
		if _, ok := out[key]; !ok {
			t.Errorf("usage response missing key %q: %v", key, out)
		}
	}
	// No auth/tenant middleware means mw.TenantFrom returns "default".
	if out["tenant"] != "default" {
		t.Errorf("tenant: want %q, got %v", "default", out["tenant"])
	}
}

// TestAdminListWebhookFailures verifies that GET /v1/admin/webhook-failures
// returns 200 with an empty failures list on a fresh database, and that the
// optional ?limit= query parameter is accepted without error.
func TestAdminListWebhookFailures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "ops.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg, _ := auth.Parse("")
	adm := NewAdminHandler(nil, repo, reg)

	r := chi.NewRouter()
	r.Get("/v1/admin/webhook-failures", adm.ListWebhookFailures)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	// Empty list on fresh DB.
	resp, body := req(t, "GET", srv.URL+"/v1/admin/webhook-failures", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook-failures: status=%d body=%s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("webhook-failures decode: %v", err)
	}
	failures, ok := out["failures"]
	if !ok {
		t.Fatalf("response missing 'failures' key: %v", out)
	}
	// The value must be a JSON array or null (nil slice encodes as null).
	switch failures.(type) {
	case []any, nil:
		// ok
	default:
		t.Fatalf("'failures' must be an array or null, got: %T %v", failures, failures)
	}

	// With explicit ?limit=10 should still succeed.
	resp, body = req(t, "GET", srv.URL+"/v1/admin/webhook-failures?limit=10", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook-failures?limit=10: status=%d body=%s", resp.StatusCode, body)
	}
}

// TestAdminListJobsAndRetry exercises the job listing and retry endpoints.
func TestAdminListJobsAndRetry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "ops.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg, _ := auth.Parse("")
	adm := NewAdminHandler(nil, repo, reg)

	r := chi.NewRouter()
	r.Get("/v1/admin/jobs", adm.ListJobs)
	r.Post("/v1/admin/jobs/{id}/retry", adm.RetryJob)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	// GET /v1/admin/jobs → 200 with empty jobs list and stats map.
	resp, body := req(t, "GET", srv.URL+"/v1/admin/jobs", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list jobs: status=%d body=%s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("list jobs decode: %v", err)
	}
	if _, ok := out["jobs"]; !ok {
		t.Errorf("response missing 'jobs' key: %v", out)
	}
	if _, ok := out["stats"]; !ok {
		t.Errorf("response missing 'stats' key: %v", out)
	}
	jobs, ok := out["jobs"].([]any)
	if !ok {
		t.Fatalf("'jobs' is not an array: %T %v", out["jobs"], out["jobs"])
	}
	if len(jobs) != 0 {
		t.Errorf("want empty jobs list, got %d entries", len(jobs))
	}

	// GET /v1/admin/jobs?limit=50 → 200.
	resp, body = req(t, "GET", srv.URL+"/v1/admin/jobs?limit=50", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list jobs limit=50: status=%d body=%s", resp.StatusCode, body)
	}

	// POST retry on a nonexistent ID is a no-op (no error from repo).
	resp, body = req(t, "POST", srv.URL+"/v1/admin/jobs/999/retry", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry nonexistent job: status=%d body=%s", resp.StatusCode, body)
	}

	// POST retry with a non-integer ID → 400.
	resp, body = req(t, "POST", srv.URL+"/v1/admin/jobs/badid/retry", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("retry bad id: want 400, got %d body=%s", resp.StatusCode, body)
	}
}
