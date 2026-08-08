package integration

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/api/s3compat"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// testShareSecret32 is the 32-byte ShareSecret literal required by
// access.NewManager's hard check (manager.go:44-45); access_test.go:32
// precedent.
var testShareSecret32 = []byte("0123456789abcdef0123456789abcdef")

// principalMW injects the principal named by X-Test-Principal into the
// server-side request context — the same shape as the auth middleware chain
// (auth_middleware.go:183); a client-side WithPrincipal context never crosses
// the HTTP boundary.
func principalMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subj := r.Header.Get("X-Test-Principal"); subj != "" {
			p := access.Principal{SubjectID: subj, TenantID: "default", Kind: access.PrincipalUser}
			if subj == "root" {
				p.Scopes = []string{"admin"}
			}
			r = r.WithContext(access.WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

// outboxCountFor counts event_outbox rows for an origin object + event type.
func outboxCountFor(t *testing.T, dsn string, originID int64, eventType repository.OutboxEventType) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dsn: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM event_outbox WHERE origin_id=? AND event_type=?`,
		originID, string(eventType)).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

// outboxPayloadFor returns the raw payload of the newest outbox row for the
// origin object + event type ("" when absent).
func outboxPayloadFor(t *testing.T, dsn string, originID int64, eventType repository.OutboxEventType) string {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dsn: %v", err)
	}
	defer db.Close()
	var payload string
	err = db.QueryRow(
		`SELECT payload FROM event_outbox WHERE origin_id=? AND event_type=? ORDER BY id DESC LIMIT 1`,
		originID, string(eventType)).Scan(&payload)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	return payload
}

// TestCompositionRevokeRestoreParity (AC-6): one access.Manager shared by the
// S3 adapter gate and the FileService gate. Mid-session revocation flips both
// S3 DELETE and REST admin delete to 403; re-granting restores 204 with a
// valid vault.file.deleted@1.1 outbox fact.
func TestCompositionRevokeRestoreParity(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "parity.db")
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
	svc.WithAuthorizer(manager) // main.go:215 parity

	authReg, _ := auth.Parse("")
	r := chi.NewRouter()
	r.Mount("/s3", s3compat.NewRouter(svc, logger, manager)) // http.go:120 parity
	r.Mount("/v1", rest.NewRouter(svc, repo, nil, nil, nil, nil, authReg, logger,
		false, nil, nil, 0, false,
		func(h *rest.Handler) { h.WithAccessManager(manager, "http://public.invalid") }))
	// Principal injection is server-side, like the auth middleware chain
	// (auth_middleware.go:183): a client-side WithPrincipal context never
	// crosses the HTTP boundary. X-Test-Principal names the SubjectID.
	ts := httptest.NewServer(principalMW(r))
	t.Cleanup(ts.Close)

	// Principal P: plain user, no scopes/roles — an admin-scoped principal
	// would short-circuit isAdministrator and break the revocation step.
	// admin drives setup (ACL management, object creation, survival reads):
	// the service gate (WithAuthorizer) also gates PUT/GET, so setup needs a
	// principal that clears isAdministrator. Direct manager calls use the
	// in-process admin context; HTTP requests use X-Test-Principal.
	admin := access.WithPrincipal(ctx, access.Principal{
		SubjectID: "root", TenantID: "default", Kind: access.PrincipalUser,
		Scopes: []string{"admin"},
	})
	s3URL := ts.URL + "/s3/default/k.txt"
	restURL := ts.URL + "/v1/files/k.txt"

	putObject := func() repository.Object {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, s3URL, strings.NewReader("payload"))
		req.Header.Set("X-Test-Principal", "root")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("S3 PUT status=%d", resp.StatusCode)
		}
		obj, err := repo.GetObject(ctx, "default", "default", "k.txt")
		if err != nil {
			t.Fatal(err)
		}
		return obj
	}
	deleteVia := func(method, url string) int {
		t.Helper()
		req, _ := http.NewRequest(method, url, nil)
		req.Header.Set("X-Test-Principal", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	grant := func() access.ACLEntry {
		t.Helper()
		entry, err := manager.PutACL(admin, access.ACLEntry{
			TenantID: "default", Bucket: "default",
			ResourceKind: access.ResourceBucket, PrincipalType: access.PrincipalTypeUser,
			PrincipalID: "alice", Action: access.ActionDelete, Effect: access.EffectAllow,
		})
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}

	// 1) Grant → S3 DELETE 204 + exactly one valid deleted@1.1 outbox fact.
	entry := grant()
	obj := putObject()
	if code := deleteVia(http.MethodDelete, s3URL); code != http.StatusNoContent {
		t.Fatalf("S3 DELETE with grant: status=%d want 204", code)
	}
	if n := outboxCountFor(t, dsn, obj.ID, repository.EventTypeFileDeleted11); n != 1 {
		t.Fatalf("deleted@1.1 rows=%d want 1", n)
	}
	if n := outboxCountFor(t, dsn, obj.ID, repository.EventTypeFileNotify11); n != 1 {
		t.Fatalf("notify@1.1 rows=%d want 1", n)
	}
	payload := outboxPayloadFor(t, dsn, obj.ID, repository.EventTypeFileDeleted11)
	for _, want := range []string{
		`"schema_version":"1.1"`, `"tenant":"default"`, `"bucket":"default"`,
		`"key":"k.txt"`, `"actor":"alice"`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("deleted@1.1 payload missing %s: %s", want, payload)
		}
	}

	// 2) Mid-session revocation (same ctx, no token refresh) → both 403.
	putObject() // restore the object; revoke happens before any further delete
	if err := manager.DeleteACL(admin, "default", entry.ID); err != nil {
		t.Fatal(err)
	}
	if code := deleteVia(http.MethodDelete, s3URL); code != http.StatusForbidden {
		t.Fatalf("S3 DELETE after revoke: status=%d want 403", code)
	}
	if code := deleteVia(http.MethodDelete, restURL); code != http.StatusForbidden {
		t.Fatalf("REST DELETE after revoke: status=%d want 403 (parity)", code)
	}
	if n := outboxCountFor(t, dsn, obj.ID, repository.EventTypeFileDeleted11); n != 1 {
		t.Fatalf("revoked deletes wrote outbox rows (now %d, want 1)", n)
	}
	// The object survives the revoked deletes (checked as admin: alice has no
	// read ACL and the service read gate would 403 her).
	getReq, _ := http.NewRequest(http.MethodGet, restURL, nil)
	getReq.Header.Set("X-Test-Principal", "root")
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("object should survive revoked deletes, GET status=%d", getResp.StatusCode)
	}

	// 3) Re-grant → S3 DELETE 204 + a fresh valid outbox fact.
	grant()
	obj2 := putObject()
	if code := deleteVia(http.MethodDelete, s3URL); code != http.StatusNoContent {
		t.Fatalf("S3 DELETE after re-grant: status=%d want 204", code)
	}
	if n := outboxCountFor(t, dsn, obj2.ID, repository.EventTypeFileDeleted11); n != 1 {
		t.Fatalf("re-grant deleted@1.1 rows=%d want 1", n)
	}
	if payload := outboxPayloadFor(t, dsn, obj2.ID, repository.EventTypeFileDeleted11); !strings.Contains(payload, `"schema_version":"1.1"`) {
		t.Fatalf("re-grant payload invalid: %s", payload)
	}
}
