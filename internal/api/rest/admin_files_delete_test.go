package rest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// adminDeleteEnv is the shared real-repo+storage+svc+router environment for
// the admin files delete handler tests. The handler's svc is a concrete
// *service.FileService (no stub possible), so all assertions are behavioral.
type adminDeleteEnv struct {
	ts    *httptest.Server
	repo  repository.Repository
	svc   *service.FileService
	store storage.Storage
	dsn   string
}

// newAdminDeleteEnv builds a production-shaped REST router (real route table)
// behind the auth + tenant middleware chain. authKeys "" disables the registry
// (baseline CI shape); a non-empty value enables it (requireAdmin becomes
// real). manager, when non-nil, is installed as the svc authorizer (F2/F4).
func newAdminDeleteEnv(t *testing.T, authKeys string, manager *access.Manager, store storage.Storage) *adminDeleteEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "admin-delete.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if store == nil {
		store, err = storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
		if err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.NewFileService(store, repo, logger)
	if manager != nil {
		svc.WithAuthorizer(manager)
	} else {
		// No manager: keep the CI baseline (all actions allowed) so these
		// behavioral tests exercise the admin endpoint, not the
		// fail-closed delete gate (which is covered by dedicated tests).
		svc.WithAuthorizer(allowAllProvider{})
	}
	reg, err := auth.Parse(authKeys)
	if err != nil {
		t.Fatal(err)
	}
	v1 := NewRouter(svc, repo, nil, nil, nil, nil, reg, logger, false, nil, nil, 0, false)
	root := chi.NewRouter()
	root.Use(reg.Middleware())
	root.Use(mw.Tenant)
	root.Mount("/v1", v1)
	ts := httptest.NewServer(root)
	t.Cleanup(ts.Close)
	return &adminDeleteEnv{ts: ts, repo: repo, svc: svc, store: store, dsn: dsn}
}

// putObject seeds an object through the real REST upload path.
func (e *adminDeleteEnv) putObject(t *testing.T, tenant, key string) repository.Object {
	t.Helper()
	hdr := map[string]string{"Authorization": "Bearer opsecret"}
	if tenant != "" {
		hdr[mw.TenantHeader] = tenant
	}
	resp, body := req(t, http.MethodPut, e.ts.URL+"/v1/files/"+key, []byte("payload"), hdr)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %s (tenant=%s): status=%d body=%s", key, tenant, resp.StatusCode, body)
	}
	obj, err := e.repo.GetObject(context.Background(), tenant, "default", key)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func (e *adminDeleteEnv) outboxFactCount(t *testing.T, originID int64, eventType repository.OutboxEventType) int {
	t.Helper()
	has, err := e.repo.HasEventOutboxFact(context.Background(), originID, eventType)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		return 0
	}
	return 1
}

func (e *adminDeleteEnv) auditDeleteRows(t *testing.T) []repository.AuditEntry {
	t.Helper()
	rows, err := e.repo.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []repository.AuditEntry
	for _, row := range rows {
		if row.Action == repository.AuditActionFileDelete {
			out = append(out, row)
		}
	}
	return out
}

// assertNoWriteSideEffects asserts a rejected delete left zero audit rows and
// zero outbox facts for the object (F1/F2/F3/F5/F7/F8).
func (e *adminDeleteEnv) assertNoWriteSideEffects(t *testing.T, obj repository.Object) {
	t.Helper()
	for _, et := range []repository.OutboxEventType{
		repository.EventTypeFileDeleted11, repository.EventTypeFileNotify11,
	} {
		if n := e.outboxFactCount(t, obj.ID, et); n != 0 {
			t.Errorf("outbox fact %s rows = %d, want 0", et, n)
		}
	}
	if rows := e.auditDeleteRows(t); len(rows) != 0 {
		t.Errorf("file.delete audit rows = %d, want 0: %+v", len(rows), rows)
	}
}

// TestAdminDeleteFile_RequireAdmin — AC-1: anonymous → 401 (enabled registry),
// non-admin scope → 403 Forbidden before any write path, operator key → 204.
func TestAdminDeleteFile_RequireAdmin(t *testing.T) {
	env := newAdminDeleteEnv(t, "user:default:read+write,opsecret:*:admin", nil, nil)
	obj := env.putObject(t, "acme", "k.txt")

	// Anonymous: enabled registry rejects before the router.
	resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous DELETE status=%d body=%s, want 401", resp.StatusCode, body)
	}

	// write scope but no admin scope → requireAdmin 403, object untouched.
	resp, body = req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil,
		map[string]string{"Authorization": "Bearer user"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin DELETE status=%d body=%s, want 403", resp.StatusCode, body)
	}
	var envErr struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envErr); err != nil {
		t.Fatal(err)
	}
	if envErr.Error.Code != "Forbidden" || !strings.Contains(envErr.Error.Message, "admin scope required") {
		t.Errorf("403 envelope = %+v, want code Forbidden + 'admin scope required'", envErr)
	}
	if _, err := env.repo.GetObject(context.Background(), "acme", "default", "k.txt"); err != nil {
		t.Errorf("object must survive rejected delete: %v", err)
	}
	env.assertNoWriteSideEffects(t, obj)

	// Operator key → 204.
	resp, body = req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil,
		map[string]string{"Authorization": "Bearer opsecret"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("operator DELETE status=%d body=%s, want 204", resp.StatusCode, body)
	}
	if _, err := env.repo.GetObject(context.Background(), "acme", "default", "k.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetObject after admin hard delete = %v, want ErrNotFound", err)
	}
}

// TestAdminDeleteFile_RouteAndPassthrough — AC-1: the route resolves the
// tenant from the path (not the header), passes ?hard=1 through to the shared
// svc.Delete path (blob gone on hard, deleted_at set on soft), and handles
// multi-segment keys. Empty tenant → 400 InvalidArgument (F13).
func TestAdminDeleteFile_RouteAndPassthrough(t *testing.T) {
	t.Run("hard delete removes blob and row", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil)
		obj := env.putObject(t, "acme", "docs/sub/a.txt")
		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/docs/sub/a.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status=%d body=%s, want 204", resp.StatusCode, body)
		}
		if _, err := env.repo.GetObject(context.Background(), "acme", "default", "docs/sub/a.txt"); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("GetObject = %v, want ErrNotFound", err)
		}
		if _, err := env.store.Stat(context.Background(), obj.StorageKey); err == nil {
			t.Error("storage blob still present after hard delete")
		}
		if rows := env.auditDeleteRows(t); len(rows) != 1 || rows[0].Detail != "hard" {
			t.Errorf("audit rows = %+v, want exactly 1 hard", rows)
		}
	})

	t.Run("soft delete keeps blob, sets deleted_at", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil)
		obj := env.putObject(t, "acme", "k.txt")
		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status=%d body=%s, want 204", resp.StatusCode, body)
		}
		if _, err := env.repo.GetObject(context.Background(), "acme", "default", "k.txt"); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("GetObject (deleted_at filter) = %v, want ErrNotFound", err)
		}
		got, err := env.repo.GetObjectByID(context.Background(), obj.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.DeletedAt == nil {
			t.Error("deleted_at not set after soft delete")
		}
		if _, err := env.store.Stat(context.Background(), obj.StorageKey); err != nil {
			t.Errorf("storage blob must survive soft delete: %v", err)
		}
		if rows := env.auditDeleteRows(t); len(rows) != 1 || rows[0].Detail != "soft" {
			t.Errorf("audit rows = %+v, want exactly 1 soft", rows)
		}
	})

	t.Run("tenant comes from the path, not X-Aero-Tenant", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil)
		obj := env.putObject(t, "acme", "k.txt")
		// Operator key (tenant=*) is not pinned by the middleware; the header
		// names a different tenant but the path must win.
		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer opsecret", mw.TenantHeader: "other"})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status=%d body=%s, want 204", resp.StatusCode, body)
		}
		if _, err := env.repo.GetObject(context.Background(), "acme", "default", "k.txt"); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("acme object must be deleted via path tenant: %v", err)
		}
		_ = obj
	})

	t.Run("empty tenant rejected with 400 InvalidArgument (F13)", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil)
		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files//k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s, want 400", resp.StatusCode, body)
		}
		var envErr struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &envErr); err != nil {
			t.Fatal(err)
		}
		if envErr.Error.Code != "InvalidArgument" {
			t.Errorf("error code = %q, want InvalidArgument", envErr.Error.Code)
		}
	})
}

// failDeleteStore wraps storage.Storage with a failing Delete — the F7 seam
// (storage-first ordering: blob delete failure must precede any metadata
// write).
type failDeleteStore struct {
	storage.Storage
}

func (f failDeleteStore) Delete(ctx context.Context, key string) error {
	return fmt.Errorf("storage delete simulated failure: %s", key)
}

// TestAdminDeleteFile_ErrorMapping — F1–F8 table-driven coverage for the new
// handler path. Each row builds its own environment; the shared seam
// assertions are behavioral (object/outbox/audit residue).
func TestAdminDeleteFile_ErrorMapping(t *testing.T) {
	ctx := context.Background()

	t.Run("F1 non-admin key → 403 Forbidden", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "user:default:read+write,opsecret:*:admin", nil, nil)
		obj := env.putObject(t, "acme", "k.txt")
		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer user"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d body=%s, want 403", resp.StatusCode, body)
		}
		env.assertNoWriteSideEffects(t, obj)
	})

	t.Run("F2 tenant-scoped admin key cross-tenant → 403 tenant_mismatch", func(t *testing.T) {
		manager, err := access.NewManager(envRepo(t).(access.Store), access.Config{
			Enabled: true, DefaultPolicy: access.DefaultTenant,
			ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
		})
		if err != nil {
			t.Fatal(err)
		}
		env := newAdminDeleteEnv(t, "adm:acme:admin,opsecret:*:admin", manager, nil)
		acmeObj := env.putObject(t, "acme", "k.txt")
		otherObj := env.putObject(t, "other", "k.txt")

		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/other/k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer adm"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d body=%s, want 403", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "tenant_mismatch") {
			t.Errorf("403 body %q missing tenant_mismatch reason", body)
		}
		// Both tenant copies untouched, zero write side effects.
		if _, err := env.repo.GetObject(ctx, "acme", "default", "k.txt"); err != nil {
			t.Errorf("acme copy must survive: %v", err)
		}
		if _, err := env.repo.GetObject(ctx, "other", "default", "k.txt"); err != nil {
			t.Errorf("other copy must survive: %v", err)
		}
		env.assertNoWriteSideEffects(t, acmeObj)
		env.assertNoWriteSideEffects(t, otherObj)

		// Positive arm: same-tenant admin key deletes its own tenant.
		resp, body = req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer adm"})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("same-tenant arm status=%d body=%s, want 204", resp.StatusCode, body)
		}
		// Operator key deletes the other tenant.
		resp, body = req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/other/k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("operator arm status=%d body=%s, want 204", resp.StatusCode, body)
		}
		// X-Aero-Tenant conflict with the tenant-scoped key → middleware 403
		// before the handler.
		resp, _ = req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt", nil,
			map[string]string{"Authorization": "Bearer adm", mw.TenantHeader: "other"})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("conflicting header status=%d, want 403 (pre-handler)", resp.StatusCode)
		}
	})

	t.Run("F3 missing object → 404, idempotent retry leaves no residue", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil)
		for i := 0; i < 2; i++ {
			resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/missing.txt?hard=1", nil,
				map[string]string{"Authorization": "Bearer opsecret"})
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("attempt %d status=%d body=%s, want 404", i, resp.StatusCode, body)
			}
		}
		if rows := env.auditDeleteRows(t); len(rows) != 0 {
			t.Errorf("audit rows = %d, want 0 after 404s", len(rows))
		}
	})

	t.Run("F5 WORM retention → 409 ObjectLocked (not 412)", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil)
		obj := env.putObject(t, "acme", "k.txt")
		if _, err := env.svc.SetObjectRetention(ctx, "acme", service.DefaultBucket, "k.txt", "",
			"GOVERNANCE", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status=%d body=%s, want 409", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "ObjectLocked") {
			t.Errorf("409 body %q missing ObjectLocked code", body)
		}
		env.assertNoWriteSideEffects(t, obj)
		got, err := env.repo.GetObject(ctx, "acme", "default", "k.txt")
		if err != nil || got.LockedUntil == nil {
			t.Errorf("object must keep its lock after rejected delete: err=%v locked=%v", err, got.LockedUntil)
		}
		// Soft delete (no ?hard=1) is not blocked by retention.
		resp, body = req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("soft arm status=%d body=%s, want 204", resp.StatusCode, body)
		}
	})

	t.Run("F6 corrupt mapping → 410, delete path itself succeeds (204)", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil)
		// Direct writeError pin: the new handler's error plumbing maps
		// ErrObjectCorrupt exactly like the REST handler.
		rec := httptest.NewRecorder()
		adm := &AdminHandler{svc: env.svc, repo: env.repo, reg: mustRegistry(t, "opsecret:*:admin")}
		req0 := httptest.NewRequest(http.MethodDelete, "http://x/v1/admin/files/acme/k.txt", nil)
		adm.writeError(rec, req0, service.ErrObjectCorrupt)
		if rec.Code != http.StatusGone {
			t.Fatalf("writeError(ErrObjectCorrupt) status=%d, want 410", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "ObjectCorrupt") {
			t.Errorf("410 body %q missing ObjectCorrupt code", rec.Body.String())
		}
		// Behavior: a scrub-marked corrupt object is still deletable through
		// the admin path (410 is a Get/Stat-time semantic; delete must not
		// introduce a new failure surface).
		env.putObject(t, "acme", "bad.txt")
		if err := env.repo.SetObjectMetaKey(ctx, "acme", "default", "bad.txt", "_aero_scrub_status", "corrupt"); err != nil {
			t.Fatal(err)
		}
		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/bad.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("corrupt-object delete status=%d body=%s, want 204", resp.StatusCode, body)
		}
	})

	t.Run("F7 storage blob delete failure → 500, zero residue (storage-first)", func(t *testing.T) {
		// The failing store is installed at construction: the router holds the
		// concrete *service.FileService, so the store must be wrong from the
		// start (Put still works — only Delete fails).
		realStore, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, failDeleteStore{Storage: realStore})
		obj := env.putObject(t, "acme", "k.txt")
		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s, want 500", resp.StatusCode, body)
		}
		if _, err := env.repo.GetObject(ctx, "acme", "default", "k.txt"); err != nil {
			t.Errorf("object must survive storage failure: %v", err)
		}
		env.assertNoWriteSideEffects(t, obj)
	})

	t.Run("F8 audit insert failure → 500, full tx rollback", func(t *testing.T) {
		env := newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil)
		obj := env.putObject(t, "acme", "k.txt")

		db, err := sql.Open("sqlite", env.dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`ALTER TABLE audit_log RENAME TO audit_log_bak`); err != nil {
			t.Fatalf("rename audit_log: %v", err)
		}

		resp, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/k.txt?hard=1", nil,
			map[string]string{"Authorization": "Bearer opsecret"})
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s, want 500", resp.StatusCode, body)
		}
		// Restore the table before the residue assertions (ListAudit reads
		// audit_log).
		if _, err := db.Exec(`ALTER TABLE audit_log_bak RENAME TO audit_log`); err != nil {
			t.Fatalf("rename audit_log back: %v", err)
		}
		// The whole delete transaction must have rolled back: object row,
		// outbox facts, audit row — zero residue.
		if _, err := env.repo.GetObject(ctx, "acme", "default", "k.txt"); err != nil {
			t.Errorf("object must survive audit failure (tx rollback): %v", err)
		}
		env.assertNoWriteSideEffects(t, obj)
	})
}

// envRepo builds a bare migrated repo for manager construction (F2 needs the
// access.Store cast; the manager is built before the env so it can be shared).
func envRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "mgr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return repo
}

func mustRegistry(t *testing.T, keys string) *auth.Registry {
	t.Helper()
	reg, err := auth.Parse(keys)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
