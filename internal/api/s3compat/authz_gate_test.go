package s3compat

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// ── Test doubles (AuthorizationProvider stubs) ──────────────────────────────

type allowAllProvider struct{}

func (allowAllProvider) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: true, Reason: "test_allow_all"}, nil
}

type denyAllProvider struct{}

func (denyAllProvider) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: false, Reason: "test_deny_all"}, nil
}

// errProvider fails closed with a provider error (AC-2).
type errProvider struct{ err error }

func (p errProvider) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{}, p.err
}

// keyDenyProvider denies exactly the keys in the map, allowing everything
// else (AC-3 per-key batch semantics).
type keyDenyProvider struct{ denied map[string]bool }

func (p keyDenyProvider) Authorize(_ context.Context, _ access.Principal, _ access.Action, res access.Resource) (access.Decision, error) {
	if p.denied[res.Key] {
		return access.Decision{Allowed: false, Reason: "test_deny_key"}, nil
	}
	return access.Decision{Allowed: true, Reason: "test_allow_key"}, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// testShareSecret is the proven-constructing 32-byte ShareSecret literal
// (manager.go:44-45 requires len >= 32; access_test.go:32 precedent).
var testShareSecret = []byte("0123456789abcdef0123456789abcdef")

// newAuthzServer builds a repo-backed S3 server with the given provider and
// returns the server, repo, dsn (for raw outbox counting) and service.
func newAuthzServer(t *testing.T, authz AuthorizationProvider) (*httptest.Server, repository.Repository, string, *service.FileService) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "authz.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	srv := httptest.NewServer(NewRouter(svc, nil, authz))
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv, repo, dsn, svc
}

// newRealManager builds the production-shaped access.Manager over the repo
// (DefaultPolicy=deny, ShareSecret >= 32 bytes).
func newRealManager(t *testing.T, repo repository.Repository) *access.Manager {
	t.Helper()
	store, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	manager, err := access.NewManager(store, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultDeny, ShareSecret: testShareSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

// principalMiddleware injects the principal named by the X-Test-Principal
// header into the server-side request context — the same shape as the auth
// middleware chain (auth_middleware.go:183); a client-side WithPrincipal
// context never crosses the HTTP boundary.
func principalMiddleware(next http.Handler) http.Handler {
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

// doAs issues a request with an X-Test-Principal header (server-side
// principal injection).
func doAs(t *testing.T, method, url string, body []byte, principal string) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Test-Principal", principal)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// outboxCount counts event_outbox rows for one origin object + event type via
// a second connection to the same sqlite file (placeholders are ? for sqlite).
func outboxCount(t *testing.T, dsn string, originID int64, eventType repository.OutboxEventType) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open outbox dsn: %v", err)
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

func assertAccessDenied(t *testing.T, resp *http.Response, body []byte) {
	t.Helper()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "<Code>AccessDenied</Code>") {
		t.Fatalf("missing AccessDenied code body=%s", body)
	}
}

// ── AC-1: provider wired — deny or no principal → 403, no bucket policy ─────

func TestDeleteDeniedWithoutBucketPolicy(t *testing.T) {
	ctx := context.Background()
	srv, _, _, svc := newAuthzServer(t, denyAllProvider{})
	base := srv.URL

	// Unversioned bucket: plain delete would call svc.Delete.
	if resp, _ := do(t, "PUT", base+"/plain/k.txt", []byte("x"), nil); resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	// Versioned bucket: ?versionId delete + delete-marker creation paths.
	if err := svc.SetBucketVersioning(ctx, "default", "ver", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "default", "ver", "k.txt", strings.NewReader("one"), 3, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "default", "ver", "k.txt", strings.NewReader("two"), 3, service.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, url string }{
		{"plain", "/plain/k.txt"},
		{"versionId", "/ver/k.txt?versionId=" + v1.VersionID},
		{"delete-marker", "/ver/k.txt"},
	} {
		resp, body := do(t, "DELETE", base+tc.url, nil, nil)
		assertAccessDenied(t, resp, body)
	}
	// The object is untouched by the denied deletes.
	if resp, _ := do(t, "GET", base+"/plain/k.txt", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("object should survive denied deletes, got %d", resp.StatusCode)
	}
}

func TestDeleteDeniedWhenNoPrincipal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := mustRepo(t, ctx, dir)
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, nil)
	// Principal injection is server-side (auth_middleware.go:183 pattern):
	// a client-side WithPrincipal context never reaches r.Context().
	srv := httptest.NewServer(principalMiddleware(NewRouter(svc, nil, newRealManager(t, repo))))
	t.Cleanup(srv.Close)
	base := srv.URL
	if resp, _ := do(t, "PUT", base+"/b/k.txt", []byte("x"), nil); resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	// No principal in the request context → zero-value principal →
	// manager denies with missing_principal (authorizer.go:26-27).
	resp, body := do(t, "DELETE", base+"/b/k.txt", nil, nil)
	assertAccessDenied(t, resp, body)
	// Principal without any ACL → default_deny.
	resp, body = doAs(t, "DELETE", base+"/b/k.txt", nil, "alice")
	assertAccessDenied(t, resp, body)
}

// ── AC-2: provider error → 403 fail-closed, never 500 ───────────────────────

func TestDeleteProviderErrorIs403Not500(t *testing.T) {
	srv, _, _, _ := newAuthzServer(t, errProvider{err: errors.New("pdp outage")})
	base := srv.URL
	do(t, "PUT", base+"/b/k.txt", []byte("x"), nil)
	do(t, "PUT", base+"/b/k2.txt", []byte("y"), nil)

	// Single delete: 403 AccessDenied (V3 flip point: was InternalError/500).
	resp, body := do(t, "DELETE", base+"/b/k.txt", nil, nil)
	assertAccessDenied(t, resp, body)
	if strings.Contains(string(body), "InternalError") {
		t.Fatalf("provider error leaked as InternalError: %s", body)
	}

	// Batch delete: 200 shell + per-key AccessDenied.
	req, _ := xml.Marshal(deleteRequest{Objects: []deleteRequestObject{{Key: "k.txt"}, {Key: "k2.txt"}}})
	resp, body = do(t, "POST", base+"/b/?delete", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d want 200 body=%s", resp.StatusCode, body)
	}
	var dr deleteResult
	if err := xml.Unmarshal(body, &dr); err != nil {
		t.Fatalf("parse delete result: %v body=%s", err, body)
	}
	if len(dr.Deleted) != 0 || len(dr.Errors) != 2 {
		t.Fatalf("batch result: deleted=%d errors=%d body=%s", len(dr.Deleted), len(dr.Errors), body)
	}
	for _, e := range dr.Errors {
		if e.Code != "AccessDenied" {
			t.Fatalf("error code=%q want AccessDenied body=%s", e.Code, body)
		}
	}
}

// ── AC-3: batch per-key denial, denied keys never deleted ───────────────────

func TestBatchDeletePerKeyDenial(t *testing.T) {
	srv, _, _, _ := newAuthzServer(t, keyDenyProvider{denied: map[string]bool{"denied.txt": true}})
	base := srv.URL
	do(t, "PUT", base+"/b/allowed.txt", []byte("a"), nil)
	do(t, "PUT", base+"/b/denied.txt", []byte("d"), nil)

	req, _ := xml.Marshal(deleteRequest{Objects: []deleteRequestObject{{Key: "allowed.txt"}, {Key: "denied.txt"}}})
	resp, body := do(t, "POST", base+"/b/?delete", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d want 200 body=%s", resp.StatusCode, body)
	}
	var dr deleteResult
	if err := xml.Unmarshal(body, &dr); err != nil {
		t.Fatalf("parse delete result: %v body=%s", err, body)
	}
	if len(dr.Deleted) != 1 || dr.Deleted[0].Key != "allowed.txt" {
		t.Fatalf("deleted=%+v want exactly allowed.txt body=%s", dr.Deleted, body)
	}
	if len(dr.Errors) != 1 || dr.Errors[0].Key != "denied.txt" ||
		dr.Errors[0].Code != "AccessDenied" || dr.Errors[0].Message != "Access denied." {
		t.Fatalf("errors=%+v want denied.txt AccessDenied body=%s", dr.Errors, body)
	}
	// "without partially deleting denied keys": allowed gone, denied survives.
	if resp, _ := do(t, "GET", base+"/b/allowed.txt", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("allowed.txt should be deleted, got %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/b/denied.txt", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("denied.txt should survive, got %d", resp.StatusCode)
	}

	// All keys denied → 0 Deleted + per-key errors, nothing removed.
	srv2, _, _, _ := newAuthzServer(t, denyAllProvider{})
	base2 := srv2.URL
	do(t, "PUT", base2+"/b/x.txt", []byte("x"), nil)
	do(t, "PUT", base2+"/b/y.txt", []byte("y"), nil)
	req2, _ := xml.Marshal(deleteRequest{Objects: []deleteRequestObject{{Key: "x.txt"}, {Key: "y.txt"}}})
	resp, body = do(t, "POST", base2+"/b/?delete", req2, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d want 200 body=%s", resp.StatusCode, body)
	}
	var dr2 deleteResult
	_ = xml.Unmarshal(body, &dr2)
	if len(dr2.Deleted) != 0 || len(dr2.Errors) != 2 {
		t.Fatalf("all-denied batch: deleted=%d errors=%d body=%s", len(dr2.Deleted), len(dr2.Errors), body)
	}
	if resp, _ := do(t, "GET", base2+"/b/x.txt", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("x.txt should survive, got %d", resp.StatusCode)
	}
}

// ── AC-4: denied requests write zero outbox rows ────────────────────────────

func TestDeniedDeleteWritesNoOutboxRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "o.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	manager := newRealManager(t, repo)
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	denied := httptest.NewServer(NewRouter(svc, nil, manager))
	allowed := httptest.NewServer(NewRouter(svc, nil, allowAllProvider{}))
	t.Cleanup(func() { denied.Close(); allowed.Close() })

	// (a) Single delete denied.
	do(t, "PUT", denied.URL+"/b/k.txt", []byte("x"), nil)
	obj, err := repo.GetObject(ctx, "default", "b", "k.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp, body := do(t, "DELETE", denied.URL+"/b/k.txt", nil, nil)
	assertAccessDenied(t, resp, body)
	if n := outboxCount(t, dsn, obj.ID, repository.EventTypeFileDeleted11); n != 0 {
		t.Fatalf("denied delete wrote %d deleted@1.1 rows", n)
	}
	if n := outboxCount(t, dsn, obj.ID, repository.EventTypeFileNotify11); n != 0 {
		t.Fatalf("denied delete wrote %d notify@1.1 rows", n)
	}
	assertZeroSideEffects(t, dsn, obj)

	// (b) Batch with both keys denied.
	do(t, "PUT", denied.URL+"/b/k2.txt", []byte("y"), nil)
	obj2, err := repo.GetObject(ctx, "default", "b", "k2.txt")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := xml.Marshal(deleteRequest{Objects: []deleteRequestObject{{Key: "k.txt"}, {Key: "k2.txt"}}})
	resp, body = do(t, "POST", denied.URL+"/b/?delete", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d body=%s", resp.StatusCode, body)
	}
	for _, o := range []struct{ id int64 }{{obj.ID}, {obj2.ID}} {
		if n := outboxCount(t, dsn, o.id, repository.EventTypeFileDeleted11); n != 0 {
			t.Fatalf("denied batch wrote %d deleted@1.1 rows", n)
		}
	}

	// Control: allowed delete of the same key writes exactly 1+1 rows.
	resp, _ = do(t, "DELETE", allowed.URL+"/b/k.txt", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("control delete status=%d want 204", resp.StatusCode)
	}
	if n := outboxCount(t, dsn, obj.ID, repository.EventTypeFileDeleted11); n != 1 {
		t.Fatalf("allowed delete wrote %d deleted@1.1 rows, want 1", n)
	}
	if n := outboxCount(t, dsn, obj.ID, repository.EventTypeFileNotify11); n != 1 {
		t.Fatalf("allowed delete wrote %d notify@1.1 rows, want 1", n)
	}
}

// assertZeroSideEffects locks the FR-5 invariant: no object_events row and no
// audit_log file.delete row for the denied request.
func assertZeroSideEffects(t *testing.T, dsn string, obj repository.Object) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM object_events WHERE bucket=? AND key=? AND type='deleted'`,
		obj.Bucket, obj.Key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("denied delete emitted %d object_events rows", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action=? AND target=?`,
		"file.delete", obj.Bucket+"/"+obj.Key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("denied delete wrote %d audit_log rows", n)
	}
}

// ── AC-5: no @1.1 event on denial (bus-level) ───────────────────────────────

func TestDeniedDeleteEmitsNoEvent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(repo, logger)
	svc := service.NewFileService(store, repo, logger)
	svc.WithEventSink(bus)
	sub, unsub := bus.Subscribe()
	t.Cleanup(unsub)

	srv := httptest.NewServer(NewRouter(svc, nil, denyAllProvider{}))
	t.Cleanup(srv.Close)
	do(t, "PUT", srv.URL+"/b/k.txt", []byte("x"), nil)
	// Drain the PUT's created event — the deny assertion is about the DELETE.
	for {
		select {
		case <-sub:
		default:
			goto drained
		}
	}
drained:
	obj, err := repo.GetObject(ctx, "default", "b", "k.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp, body := do(t, "DELETE", srv.URL+"/b/k.txt", nil, nil)
	assertAccessDenied(t, resp, body)

	select {
	case ev := <-sub:
		t.Fatalf("denied delete emitted bus event %+v", ev)
	default:
	}
	has, err := repo.HasEventOutboxFact(ctx, obj.ID, repository.EventTypeFileDeleted11)
	if err != nil || has {
		t.Fatalf("HasEventOutboxFact(deleted@1.1) = %v, %v; want false", has, err)
	}
	has, err = repo.HasEventOutboxFact(ctx, obj.ID, repository.EventTypeFileNotify11)
	if err != nil || has {
		t.Fatalf("HasEventOutboxFact(notify@1.1) = %v, %v; want false", has, err)
	}
}

// ── AC-7: provider unset → default deny on delete only + regressions ────────

func TestDeleteDeniedWhenProviderUnset(t *testing.T) {
	ctx := context.Background()
	srv, _, _, svc := newAuthzServer(t, nil)
	base := srv.URL

	// Single delete (plain), ?versionId and delete-marker paths → 403.
	do(t, "PUT", base+"/b/k.txt", []byte("x"), nil)
	resp, body := do(t, "DELETE", base+"/b/k.txt", nil, nil)
	assertAccessDenied(t, resp, body)
	if err := svc.SetBucketVersioning(ctx, "default", "v", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "default", "v", "mk.txt", strings.NewReader("one"), 3, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = do(t, "DELETE", base+"/v/mk.txt?versionId="+v1.VersionID, nil, nil)
	assertAccessDenied(t, resp, body)
	resp, body = do(t, "DELETE", base+"/v/mk.txt", nil, nil)
	assertAccessDenied(t, resp, body)

	// Batch: 200 shell + per-key AccessDenied, keys untouched.
	do(t, "PUT", base+"/b/d1.txt", []byte("1"), nil)
	do(t, "PUT", base+"/b/d2.txt", []byte("2"), nil)
	req, _ := xml.Marshal(deleteRequest{Objects: []deleteRequestObject{{Key: "d1.txt"}, {Key: "d2.txt"}}})
	resp, body = do(t, "POST", base+"/b/?delete", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d want 200 body=%s", resp.StatusCode, body)
	}
	var dr deleteResult
	_ = xml.Unmarshal(body, &dr)
	if len(dr.Deleted) != 0 || len(dr.Errors) != 2 {
		t.Fatalf("batch: deleted=%d errors=%d body=%s", len(dr.Deleted), len(dr.Errors), body)
	}
	if resp, _ := do(t, "GET", base+"/b/d1.txt", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("d1.txt should survive, got %d", resp.StatusCode)
	}

	// Regression (I5): non-delete operations unaffected on the same server.
	if resp, _ := do(t, "GET", base+"/b/k.txt", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("GET regressed: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "HEAD", base+"/b/k.txt", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("HEAD regressed: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "PUT", base+"/b/new.txt", []byte("n"), nil); resp.StatusCode != 200 {
		t.Fatalf("PUT regressed: %d", resp.StatusCode)
	}

	// Non-s3:DeleteObject deletes stay exempt: ?tagging and ?uploadId.
	do(t, "PUT", base+"/b/tag.txt", []byte("t"), nil)
	if resp, _ := do(t, "DELETE", base+"/b/tag.txt?tagging", nil, nil); resp.StatusCode != 204 {
		t.Fatalf("?tagging delete regressed: %d", resp.StatusCode)
	}
	_, body = do(t, "POST", base+"/b/ab.bin?uploads", nil, nil)
	var init initiateMultipartUploadResult
	if err := xml.Unmarshal(body, &init); err != nil || init.UploadID == "" {
		t.Fatalf("parse init: %v body=%s", err, body)
	}
	if resp, _ := do(t, "DELETE", base+"/b/ab.bin?uploadId="+init.UploadID, nil, nil); resp.StatusCode != 204 {
		t.Fatalf("?uploadId abort regressed: %d", resp.StatusCode)
	}

	// Bucket-level negatives: the gate must not fire on key=="" paths.
	do(t, "PUT", base+"/nonempty", nil, nil)
	do(t, "PUT", base+"/nonempty/obj", []byte("o"), nil)
	resp, body = do(t, "DELETE", base+"/nonempty", nil, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "BucketNotEmpty") {
		t.Fatalf("non-empty bucket delete: status=%d want 409 BucketNotEmpty body=%s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "DELETE", base+"/nonempty?lifecycle", nil, nil); resp.StatusCode != 204 {
		t.Fatalf("?lifecycle bucket delete regressed: %d", resp.StatusCode)
	}
	// Empty bucket delete stays allowed (204).
	do(t, "PUT", base+"/empty", nil, nil)
	if resp, _ := do(t, "DELETE", base+"/empty", nil, nil); resp.StatusCode != 204 {
		t.Fatalf("empty bucket delete regressed: %d", resp.StatusCode)
	}
}

// mustRepo opens a fresh migrated repository for tests needing one.
func mustRepo(t *testing.T, ctx context.Context, dir string) repository.Repository {
	t.Helper()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return repo
}
