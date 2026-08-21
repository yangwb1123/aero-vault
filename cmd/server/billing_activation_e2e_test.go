package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/billing"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type billingActivationReceiver struct {
	mu       sync.Mutex
	retry    bool
	posts    [][]byte
	scopes   []string
	token    string
	first500 bool
}

func (r *billingActivationReceiver) serve(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/token":
		r.serveToken(w, req)
	case "/api/v1/metering/entitlement":
		r.writeEntitlement(w)
	case "/api/v1/metering/usage":
		r.serveUsage(w, req)
	default:
		http.NotFound(w, req)
	}
}

func (r *billingActivationReceiver) serveToken(w http.ResponseWriter, req *http.Request) {
	user, secret, ok := req.BasicAuth()
	if !ok || user != "billing-e2e-client" || secret != "billing-e2e-secret" {
		http.Error(w, "invalid client", http.StatusUnauthorized)
		return
	}
	if err := req.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if req.Form.Get("grant_type") != "client_credentials" {
		http.Error(w, "invalid grant", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.scopes = append([]string(nil), strings.Fields(req.Form.Get("scope"))...)
	r.token = "billing-e2e-token"
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"access_token":"billing-e2e-token","token_type":"Bearer","expires_in":3600}`)
}

func (r *billingActivationReceiver) writeEntitlement(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"entitlement":{"tenant_id":"acme","revision":1,"active":true,"features":{"vault":true},"limits":{"storage_bytes":{"hard":1000000},"storage_objects":{"hard":1000}},"effective_at":"2026-01-01T00:00:00Z"}}`)
}

func (r *billingActivationReceiver) serveUsage(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.posts = append(r.posts, append([]byte(nil), body...))
	shouldFail := r.retry && !r.first500
	if shouldFail {
		r.first500 = true
	}
	r.mu.Unlock()
	if shouldFail {
		http.Error(w, "temporary", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (r *billingActivationReceiver) snapshot() ([][]byte, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	posts := make([][]byte, len(r.posts))
	for i := range r.posts {
		posts[i] = append([]byte(nil), r.posts[i]...)
	}
	return posts, append([]string(nil), r.scopes...)
}

func TestBillingActivationFirstEventE2E(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		runBillingActivationScenario(t, false)
	})
	t.Run("retry_keeps_fact_id", func(t *testing.T) {
		runBillingActivationScenario(t, true)
	})
}

func runBillingActivationScenario(t *testing.T, retry bool) {
	t.Helper()
	receiver := &billingActivationReceiver{retry: retry}
	server := httptest.NewServer(http.HandlerFunc(receiver.serve))
	defer server.Close()
	dir := t.TempDir()
	bindingsFile := filepath.Join(dir, "bindings.json")
	if err := os.WriteFile(bindingsFile, []byte(`{"bindings":[{"tenant_id":"acme","client_id":"billing-e2e-client","client_secret_env":"BILLING_E2E_CLIENT_SECRET"}]}`), 0o600); err != nil {
		t.Fatalf("write bindings: %v", err)
	}
	setBillingE2EEnv(t, server.URL, bindingsFile, filepath.Join(dir, "aero.db"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	ctx := context.Background()
	repo, err := repository.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		_ = repo.Close()
		t.Fatalf("migrate repository: %v", err)
	}
	runtime, err := buildBillingRuntime(cfg, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_ = repo.Close()
		t.Fatalf("build billing runtime: %v", err)
	}
	t.Cleanup(func() {
		runtime.Close()
		_ = repo.Close()
	})
	seedActivationProjection(t, repo.(billing.Store))
	runtime.Start(ctx)
	wrapped := wrapBillingRepository(repo, runtime)
	if _, err := wrapped.AddTenantUsage(ctx, "acme", 100, 0); err != nil {
		t.Fatalf("apply first tenant usage: %v", err)
	}

	posts, scopes := waitBillingPosts(t, receiver, 1, 5*time.Second)
	if retry {
		posts, scopes = waitBillingPosts(t, receiver, 2, 5*time.Second)
	}
	if len(posts) < 1 {
		t.Fatal("billing relay did not send a usage fact")
	}
	var first struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(posts[0], &first); err != nil {
		t.Fatalf("decode first usage body: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f-]{36}\.bytes-allocated$`).MatchString(first.ID) {
		t.Fatalf("fact id=%q does not match UUID bytes-allocated format", first.ID)
	}
	if retry {
		var second struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(posts[1], &second); err != nil {
			t.Fatalf("decode retry usage body: %v", err)
		}
		if second.ID != first.ID || !bytes.Equal(posts[0], posts[1]) {
			t.Fatalf("retry changed stored fact: first=%s second=%s", first.ID, second.ID)
		}
	} else {
		time.Sleep(150 * time.Millisecond)
		if extra, _ := receiver.snapshot(); len(extra) != 1 {
			t.Fatalf("successful fact was retried: posts=%d", len(extra))
		}
	}
	wantScopes := []string{billing.ScopeEntitlementRead, billing.ScopeMeteringWrite}
	if strings.Join(scopes, "\x00") != strings.Join(wantScopes, "\x00") {
		t.Fatalf("token scopes=%v, want %v", scopes, wantScopes)
	}
	if err := runtimeReadiness(runtime, nil).Ready(ctx); err != nil {
		t.Fatalf("enabled billing runtime failed readiness: %v", err)
	}
}

func setBillingE2EEnv(t *testing.T, baseURL, bindingsFile, dsn string) {
	t.Helper()
	t.Setenv("BILLING_ENABLED", "true")
	t.Setenv("BILLING_BASE_URL", baseURL)
	t.Setenv("BILLING_TOKEN_URL", baseURL+"/token")
	t.Setenv("BILLING_BINDINGS_FILE", bindingsFile)
	t.Setenv("BILLING_E2E_CLIENT_SECRET", "billing-e2e-secret")
	t.Setenv("BILLING_ALLOW_INSECURE_HTTP", "true")
	t.Setenv("BILLING_HTTP_TIMEOUT_SECONDS", "2")
	t.Setenv("BILLING_PROJECTION_INTERVAL_SECONDS", "60")
	t.Setenv("BILLING_OUTBOX_POLL_MILLISECONDS", "20")
	t.Setenv("BILLING_OUTBOX_BATCH_SIZE", "8")
	t.Setenv("BILLING_OUTBOX_CLAIM_TTL_SECONDS", "10")
	t.Setenv("BILLING_OUTBOX_MAX_ATTEMPTS", "3")
	t.Setenv("BILLING_MAX_LAG_SECONDS", "60")
	t.Setenv("AUDIT_GOVERNANCE_ENABLED", "false")
	t.Setenv("AUDIT_GOVERNANCE_DRAIN", "false")
	t.Setenv("AUDIT_SINK_KIND", config.AuditSinkKindL0)
	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_DSN", "file:"+dsn)
	t.Setenv("STORAGE_BACKEND", "local")
	t.Setenv("STORAGE_LOCAL_ROOT", filepath.Dir(dsn)+"/objects")
	t.Setenv("PROMETHEUS_ENABLED", "false")
}

func seedActivationProjection(t *testing.T, store billing.Store) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := store.ApplyBillingProjection(context.Background(), repository.BillingProjection{
		TenantID: "acme", Revision: 1, Active: true,
		Bytes: repository.BillingLimit{Hard: 1000000}, Objects: repository.BillingLimit{Hard: 1000},
		EffectiveAt: now.Add(-time.Minute), ProjectedAt: now,
	}); err != nil {
		t.Fatalf("seed billing projection: %v", err)
	}
}

func waitBillingPosts(
	t *testing.T, receiver *billingActivationReceiver, want int, timeout time.Duration,
) ([][]byte, []string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		posts, scopes := receiver.snapshot()
		if len(posts) >= want {
			return posts, scopes
		}
		time.Sleep(10 * time.Millisecond)
	}
	posts, scopes := receiver.snapshot()
	t.Fatalf("usage posts=%d, want at least %d", len(posts), want)
	return posts, scopes
}
