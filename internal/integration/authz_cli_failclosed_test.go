package integration

// AC-4 composite e2e for the fail-closed CLI direction: the real CLI
// (cli.Run driven by AERO_* env) against a real server with the access.Manager
// mounted. No grant → denied with the real reason + object intact + zero
// outbox; grant → deletion succeeds and vault.file.deleted@1.1 is delivered;
// mid-run revoke → denied again while the server stays healthy.
//
// The parity harness's principalMW only reads X-Test-Principal, which the CLI
// never sends (cli.go do() sends Authorization + X-Aero-Tenant only), so this
// file mounts a test-local bearerPrincipalMW instead. AERO_TENANT must stay
// unset so TenantFrom falls back to "default" (middleware.go:50-54).

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/cli"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// bearerPrincipalMW maps the CLI's Authorization: Bearer <key> header to a
// server-side principal — the same shape as the auth middleware chain. "root"
// is special-cased to an admin scope for test setup (principalMW precedent);
// any other key (including "alice") gets no scopes so the real
// access.Manager decision is exercised.
func bearerPrincipalMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key != "" && key != r.Header.Get("Authorization") {
			p := access.Principal{SubjectID: key, TenantID: "default", Kind: access.PrincipalUser}
			if key == "root" {
				p.Scopes = []string{"admin"}
			}
			r = r.WithContext(access.WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

// captureStderr mirrors internal/cli/cli_test.go's helper (this package has
// none): replaces os.Stderr for the duration of fn.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint — read error does not matter here
	r.Close()
	return buf.String()
}

func TestAC4_CLIFailClosedDenialSurface(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "cli-failclosed.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.NewFileService(store, repo, logger)
	bus := events.New(repo, logger)
	svc.WithEventSink(bus)

	manager, err := access.NewManager(repo.(access.Store), access.Config{
		Enabled: true, DefaultPolicy: access.DefaultDeny, ShareSecret: testShareSecret32,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.WithAuthorizer(manager)

	authReg, _ := auth.Parse("")
	r := chi.NewRouter()
	r.Mount("/v1", rest.NewRouter(svc, repo, nil, nil, nil, nil, authReg, logger,
		false, nil, nil, 0, false,
		func(h *rest.Handler) { h.WithAccessManager(manager, "") }))
	ts := httptest.NewServer(bearerPrincipalMW(r))
	t.Cleanup(ts.Close)

	admin := access.WithPrincipal(ctx, access.Principal{
		SubjectID: "root", TenantID: "default", Kind: access.PrincipalUser,
		Scopes: []string{"admin"},
	})
	putObject := func() repository.Object {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/files/k.txt", strings.NewReader("payload"))
		req.Header.Set("Authorization", "Bearer root")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT status=%d", resp.StatusCode)
		}
		obj, err := repo.GetObject(ctx, "default", "default", "k.txt")
		if err != nil {
			t.Fatal(err)
		}
		return obj
	}
	cliDelete := func() (int, string) {
		t.Helper()
		t.Setenv("AERO_ENDPOINT", ts.URL)
		t.Setenv("AERO_API_KEY", "alice")
		t.Setenv("AERO_TENANT", "") // must stay unset → TenantFrom fallback "default"
		var code int
		errOut := captureStderr(t, func() { code = cli.Run([]string{"rm", "k.txt"}) })
		return code, errOut
	}
	getAsRoot := func() int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/files/k.txt", nil)
		req.Header.Set("Authorization", "Bearer root")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// ① No grant → fail-closed deny: the real reason on stderr, exit 1, the
	// object intact, zero outbox rows.
	obj := putObject()
	code, errOut := cliDelete()
	if code != 1 {
		t.Fatalf("rm without grant = %d; want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "forbidden: default_deny") {
		t.Fatalf("stderr %q missing real denial reason", errOut)
	}
	if n := outboxCountFor(t, dsn, obj.ID, repository.EventTypeFileDeleted11); n != 0 {
		t.Fatalf("denied delete wrote deleted@1.1 rows: %d", n)
	}
	if code := getAsRoot(); code != http.StatusOK {
		t.Fatalf("object must survive denied delete, GET=%d", code)
	}

	// ② Grant ActionDelete to alice → exit 0 + deleted@1.1 delivered with the
	// full payload contract.
	entry, err := manager.PutACL(admin, access.ACLEntry{
		TenantID: "default", Bucket: "default",
		ResourceKind: access.ResourceBucket, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionDelete, Effect: access.EffectAllow,
	})
	if err != nil {
		t.Fatal(err)
	}
	obj2 := putObject()
	code, errOut = cliDelete()
	if code != 0 {
		t.Fatalf("rm with grant = %d; want 0 (stderr %q)", code, errOut)
	}
	if n := outboxCountFor(t, dsn, obj2.ID, repository.EventTypeFileDeleted11); n != 1 {
		t.Fatalf("deleted@1.1 rows=%d want 1", n)
	}
	payload := outboxPayloadFor(t, dsn, obj2.ID, repository.EventTypeFileDeleted11)
	for _, want := range []string{
		`"schema_version":"1.1"`, `"tenant":"default"`, `"bucket":"default"`,
		`"key":"k.txt"`, `"actor":"alice"`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("deleted@1.1 payload missing %s: %s", want, payload)
		}
	}

	// ③ Mid-run revoke (no token refresh) → denied again with the reason, the
	// object intact, and the server healthy for follow-up requests.
	if err := manager.DeleteACL(admin, "default", entry.ID); err != nil {
		t.Fatal(err)
	}
	obj3 := putObject()
	code, errOut = cliDelete()
	if code != 1 {
		t.Fatalf("rm after revoke = %d; want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "forbidden: default_deny") {
		t.Fatalf("stderr %q missing reason after revoke", errOut)
	}
	if n := outboxCountFor(t, dsn, obj3.ID, repository.EventTypeFileDeleted11); n != 0 {
		t.Fatalf("revoked delete wrote deleted@1.1 rows: %d", n)
	}
	if code := getAsRoot(); code != http.StatusOK {
		t.Fatalf("object must survive revoked delete, GET=%d", code)
	}
	if code := getAsRoot(); code != http.StatusOK {
		t.Fatalf("server unhealthy after revoke, GET=%d", code)
	}
}
