// WebDAV-surface acceptance for the deletion transactional outbox
// (docs/requirements/durable-async-delete-outbox-webdav-v1.md, G1):
// a WebDAV DELETE (always hard — RemoveAll → hard=true) must commit exactly
// one audit_log row and both outbox facts (vault.file.deleted@1.1 +
// vault.file.notify@1.1) in the delete transaction (AC-1); MOVE must emit
// source-delete facts (FM-10); a lock-conflicted DELETE must produce zero
// rows (423 before any service call). Zero timing, zero goroutines, zero
// sleeps — pure post-hoc row reads on a fresh DB. Assertions are stdlib-only
// (I6); helpers are copied, never imported from internal/integration (C-10).
package webdav_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	dav "github.com/aero-vault/aero-vault/internal/api/webdav"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// newTestServerWithSvcDSN is newTestServerWithSvc plus the SQLite DSN, for
// tests that assert outbox fact state via raw SQL (the Repository interface
// exposes no outbox status/payload reader — only Claim/Complete/Retry/Prune
// and the HasEventOutboxFact bool). The existing harness is untouched.
func newTestServerWithSvcDSN(t *testing.T) (*httptest.Server, *service.FileService, string) {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "test.db")
	repo, err := repository.Open(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("repository.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("repo.Migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	h := mw.Tenant(dav.Handler("/webdav", svc, nil))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, svc, dsn
}

// G1 (AC-1): a WebDAV DELETE commits exactly one audit_log row and both
// outbox facts in the delete transaction; a DELETE of a missing key is a 404
// (x/net Stat-before-RemoveAll pre-check) and produces zero rows.
func TestWebDAVDelete_CommitsAuditAndBothFacts(t *testing.T) {
	srv, svc, dsn := newTestServerWithSvcDSN(t)
	repo := svc.Repo()
	ctx := context.Background()

	// 1) PUT then DELETE. {204,200} is a cross-version tolerance: x/net
	// v0.55.0 emits 204 only (TestDeleteRemovesResource parity).
	do(t, srv, http.MethodPut, "/webdav/gone.txt", []byte("bye"), nil)
	obj, err := repo.GetObject(ctx, "default", "default", "gone.txt")
	if err != nil {
		t.Fatalf("get object pre-delete: %v", err)
	}
	resp, _ := do(t, srv, http.MethodDelete, "/webdav/gone.txt", nil, nil)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: got %d, want 204 or 200", resp.StatusCode)
	}

	// 2) audit_log: exactly one row, pinned to the WebDAV hard-delete
	// contract. Actor is empty — no principal/RequestID middleware in this
	// harness (legal empty per deleteAuditEntry/deleteFacts).
	rows := auditRowsFor(t, repo, "default")
	if len(rows) != 1 {
		t.Fatalf("audit_log rows = %d, want exactly 1", len(rows))
	}
	if rows[0].Action != repository.AuditActionFileDelete || rows[0].Detail != "hard" ||
		rows[0].Target != "default/gone.txt" || rows[0].TenantID != "default" ||
		rows[0].Actor != "" {
		t.Fatalf("audit row mismatch: %+v", rows[0])
	}

	// 3) event_outbox: exactly 2 rows, both keyed to the deleted object.
	if n := outboxCountFor(t, dsn, obj.ID); n != 2 {
		t.Fatalf("event_outbox rows = %d, want exactly 2", n)
	}
	_, _, deletedPayload := outboxRow(t, dsn, obj.ID, "vault.file.deleted@1.1")
	assertDeletedFact(t, deletedPayload, obj)
	_, _, notifyPayload := outboxRow(t, dsn, obj.ID, "vault.file.notify@1.1")
	assertNotifyFact(t, notifyPayload, obj)

	// 4) DELETE of a missing key: 404 (Stat pre-check; RemoveAll never runs)
	// and zero new rows.
	resp, _ = do(t, srv, http.MethodDelete, "/webdav/notexist.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE missing: got %d, want 404", resp.StatusCode)
	}
	if n := len(auditRowsFor(t, repo, "default")); n != 1 {
		t.Fatalf("audit_log rows after missing-key DELETE = %d, want 1", n)
	}
	if n := outboxCountFor(t, dsn, obj.ID); n != 2 {
		t.Fatalf("event_outbox rows after missing-key DELETE = %d, want 2", n)
	}

	// 5) Subsequent GET must return 404 (TestDeleteRemovesResource semantics).
	resp, _ = do(t, srv, http.MethodGet, "/webdav/gone.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE: got %d, want 404", resp.StatusCode)
	}
}

// TestWebDAVMove_EmitsSourceDeleteFacts (FM-10): a WebDAV MOVE is
// copy-then-delete (davFS.Rename) — the source delete commits exactly 1
// audit row + 2 outbox facts for the source key. (Overwrite:T onto an
// existing dst would additionally delete dst first — 2 audit + 4 facts, x/net
// moveFiles deletes dst before Rename; not pinned here, see FM-10.)
func TestWebDAVMove_EmitsSourceDeleteFacts(t *testing.T) {
	srv, svc, dsn := newTestServerWithSvcDSN(t)
	repo := svc.Repo()
	ctx := context.Background()

	do(t, srv, http.MethodPut, "/webdav/src.txt", []byte("move me"), nil)
	src, err := repo.GetObject(ctx, "default", "default", "src.txt")
	if err != nil {
		t.Fatalf("get source pre-move: %v", err)
	}
	resp, _ := do(t, srv, "MOVE", "/webdav/src.txt", nil, map[string]string{
		"Destination": srv.URL + "/webdav/dst.txt",
		"Overwrite":   "T",
	})
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("MOVE: got %d, want 201 or 204", resp.StatusCode)
	}

	// Source-delete facts: 1 audit row + 2 outbox rows, all keyed to src.
	rows := auditRowsFor(t, repo, "default")
	if len(rows) != 1 {
		t.Fatalf("audit_log rows = %d, want exactly 1 (source delete)", len(rows))
	}
	if rows[0].Action != repository.AuditActionFileDelete || rows[0].Detail != "hard" ||
		rows[0].Target != "default/src.txt" || rows[0].TenantID != "default" {
		t.Fatalf("audit row mismatch: %+v", rows[0])
	}
	if n := outboxCountFor(t, dsn, src.ID); n != 2 {
		t.Fatalf("event_outbox rows = %d, want exactly 2", n)
	}
	_, _, deletedPayload := outboxRow(t, dsn, src.ID, "vault.file.deleted@1.1")
	assertDeletedFact(t, deletedPayload, src)
	_, _, notifyPayload := outboxRow(t, dsn, src.ID, "vault.file.notify@1.1")
	assertNotifyFact(t, notifyPayload, src)

	// Wire semantics preserved: source gone, destination has the content.
	resp, _ = do(t, srv, http.MethodGet, "/webdav/src.txt", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after MOVE (old path): got %d, want 404", resp.StatusCode)
	}
	resp, body := do(t, srv, http.MethodGet, "/webdav/dst.txt", nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "move me" {
		t.Fatalf("GET after MOVE (new path): got %d %q, want 200 %q", resp.StatusCode, string(body), "move me")
	}
}

// TestWebDAVDelete_LockConflictedNoOutbox: a lock-conflicted DELETE is
// rejected by x/net/webdav's confirmLocks BEFORE any service call — 423 and
// zero audit/outbox rows. The outbox can neither fabricate nor drop signals
// on this path.
func TestWebDAVDelete_LockConflictedNoOutbox(t *testing.T) {
	srv, svc, dsn := newTestServerWithSvcDSN(t)
	repo := svc.Repo()
	ctx := context.Background()

	do(t, srv, http.MethodPut, "/webdav/k.txt", []byte("locked"), nil)

	// LOCK the resource (lockinfo body, Depth: 0). handleLock on an existing
	// resource only consults the in-memory LockSystem — zero service writes.
	const lockInfo = `<?xml version="1.0" encoding="utf-8" ?>
<D:lockinfo xmlns:D="DAV:">
  <D:lockscope><D:exclusive/></D:lockscope>
  <D:locktype><D:write/></D:locktype>
  <D:owner>outbox-test</D:owner>
</D:lockinfo>`
	resp, _ := do(t, srv, "LOCK", "/webdav/k.txt", []byte(lockInfo), map[string]string{
		"Depth":   "0",
		"Timeout": "Second-3600",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LOCK: got %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("Lock-Token") == "" {
		t.Fatal("LOCK: missing Lock-Token header")
	}

	// DELETE without an If header → confirmLocks fails → 423.
	resp, _ = do(t, srv, http.MethodDelete, "/webdav/k.txt", nil, nil)
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("DELETE locked: got %d, want 423", resp.StatusCode)
	}

	// Zero service mutation: no audit row, no outbox row, object intact.
	if n := len(auditRowsFor(t, repo, "default")); n != 0 {
		t.Fatalf("audit_log rows = %d, want 0 (no service call)", n)
	}
	obj, err := repo.GetObject(ctx, "default", "default", "k.txt")
	if err != nil {
		t.Fatalf("get locked object: %v", err)
	}
	if n := outboxCountFor(t, dsn, obj.ID); n != 0 {
		t.Fatalf("event_outbox rows = %d, want 0", n)
	}
	resp, body := do(t, srv, http.MethodGet, "/webdav/k.txt", nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "locked" {
		t.Fatalf("GET after locked DELETE: %d %q, want 200 %q", resp.StatusCode, string(body), "locked")
	}
}

// ── shared assertion helpers (copied from internal/integration shape, C-10) ─

// auditRowsFor returns the audit_log rows for the tenant (ListAudit order).
func auditRowsFor(t *testing.T, repo repository.Repository, tenant string) []repository.AuditEntry {
	t.Helper()
	rows, err := repo.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []repository.AuditEntry
	for _, r := range rows {
		if r.TenantID == tenant {
			out = append(out, r)
		}
	}
	return out
}

// assertAuditRowFor asserts a file.delete audit row exists for the tenant
// with the expected detail.
func assertAuditRowFor(t *testing.T, repo repository.Repository, tenant, detail string) {
	t.Helper()
	for _, row := range auditRowsFor(t, repo, tenant) {
		if row.Action == repository.AuditActionFileDelete && row.Detail == detail {
			return
		}
	}
	t.Fatalf("no file.delete audit row for tenant %s (detail %s)", tenant, detail)
}

// outboxRow reads one outbox fact's id/status/payload through a raw SQLite
// connection (origin_id-scoped, newest first) — the Repository interface
// exposes no status/payload reader; the relay owns claims.
func outboxRow(t *testing.T, dsn string, originID int64, eventType string) (int64, string, []byte) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id int64
	var status string
	var payload []byte
	if err := db.QueryRow(`SELECT id, status, payload FROM event_outbox
WHERE origin_id=? AND event_type=? ORDER BY id DESC LIMIT 1`, originID, eventType).Scan(&id, &status, &payload); err != nil {
		t.Fatalf("query outbox row: %v", err)
	}
	return id, status, payload
}

// outboxStatus reads one outbox fact's status (delivery-state witness).
func outboxStatus(t *testing.T, dsn string, originID int64, eventType string) string {
	t.Helper()
	_, status, _ := outboxRow(t, dsn, originID, eventType)
	return status
}

// outboxPayload reads one outbox fact's stored payload — the byte-exact
// comparison witness for relay egress.
func outboxPayload(t *testing.T, dsn string, originID int64, eventType string) []byte {
	t.Helper()
	_, _, payload := outboxRow(t, dsn, originID, eventType)
	return payload
}

// outboxCountFor counts the outbox facts keyed to one origin object.
func outboxCountFor(t *testing.T, dsn string, originID int64) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM event_outbox WHERE origin_id=?`, originID).Scan(&n); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	return n
}

// setDeleteRule installs a bucket notification rule for tenant/bucket
// default/default (same shape as the integration fixtures). The FM-7
// ordering constraint — the rule must exist BEFORE the delete — is the
// caller's responsibility.
func setDeleteRule(t *testing.T, repo repository.Repository, url string) {
	t.Helper()
	if err := repo.SetBucketNotifications(context.Background(), "default", "default", []repository.NotificationRule{{
		ID:          "rule-1",
		Events:      []string{"s3:ObjectRemoved:Delete"},
		EndpointURL: url,
	}}); err != nil {
		t.Fatalf("set notifications: %v", err)
	}
}

// waitForBodies polls a counter until it reaches want (or times out).
func waitForBodies(t *testing.T, count func() int, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("received %d POSTs, want %d", count(), want)
}

// sequencerHexRe pins the S3-sequencer shape produced by newSequencer at
// emit time (crypto/rand 16 bytes → 32 hex chars).
var sequencerHexRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// assertDeletedFact pins a deleted@1.1 payload against the deleted object:
// envelope identity, object_id, empty actor/request_id (no principal
// middleware in the harness), and NO S3-shaped "records" field.
func assertDeletedFact(t *testing.T, body []byte, obj repository.Object) {
	t.Helper()
	var got struct {
		SchemaVersion string `json:"schema_version"`
		EventType     string `json:"event_type"`
		Tenant        string `json:"tenant"`
		Bucket        string `json:"bucket"`
		Key           string `json:"key"`
		ObjectID      int64  `json:"object_id"`
		Actor         string `json:"actor"`
		RequestID     string `json:"request_id"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("deleted body is not JSON: %v", err)
	}
	if got.SchemaVersion != "1.1" || got.EventType != "vault.file.deleted@1.1" ||
		got.Tenant != "default" || got.Bucket != "default" || got.Key != obj.Key ||
		got.ObjectID != obj.ID || got.Actor != "" || got.RequestID != "" {
		t.Fatalf("deleted payload mismatch: schema=%q type=%q tenant=%q bucket=%q key=%q object_id=%d actor=%q request_id=%q",
			got.SchemaVersion, got.EventType, got.Tenant, got.Bucket, got.Key, got.ObjectID, got.Actor, got.RequestID)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("deleted body is not a JSON object: %v", err)
	}
	if _, ok := m["records"]; ok {
		t.Fatal("deleted@1.1 payload must not carry an S3 'records' field")
	}
}

// assertNotifyFact pins a notify@1.1 payload (stored row or relay wire bytes
// — the relay POSTs the row verbatim) against the deleted object: envelope
// identity, S3 record eventName, and the object pins size/eTag/versionId/
// sequencer captured at emit time.
func assertNotifyFact(t *testing.T, body []byte, obj repository.Object) {
	t.Helper()
	var got struct {
		SchemaVersion string `json:"schema_version"`
		EventType     string `json:"event_type"`
		Tenant        string `json:"tenant"`
		Bucket        string `json:"bucket"`
		Key           string `json:"key"`
		Records       []struct {
			EventName string `json:"eventName"`
			S3        struct {
				Object struct {
					Key       string `json:"key"`
					Size      int64  `json:"size"`
					ETag      string `json:"eTag"`
					VersionID string `json:"versionId"`
					Sequencer string `json:"sequencer"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("notify body is not JSON: %v", err)
	}
	if got.SchemaVersion != "1.1" || got.EventType != "vault.file.notify@1.1" ||
		got.Tenant != "default" || got.Bucket != "default" || got.Key != obj.Key {
		t.Fatalf("notify envelope identity mismatch: schema=%q type=%q tenant=%q bucket=%q key=%q",
			got.SchemaVersion, got.EventType, got.Tenant, got.Bucket, got.Key)
	}
	if len(got.Records) != 1 || got.Records[0].EventName != "s3:ObjectRemoved:Delete" {
		t.Fatalf("notify records mismatch: %+v", got.Records)
	}
	o := got.Records[0].S3.Object
	if o.Key != obj.Key || o.Size != obj.Size || o.ETag != obj.ETag || o.VersionID != obj.VersionID {
		t.Fatalf("notify object mismatch: key=%q size=%d eTag=%q versionId=%q, want key=%q size=%d eTag=%q versionId=%q",
			o.Key, o.Size, o.ETag, o.VersionID, obj.Key, obj.Size, obj.ETag, obj.VersionID)
	}
	if !sequencerHexRe.MatchString(o.Sequencer) {
		t.Fatalf("notify sequencer %q does not match ^[0-9a-f]{32}$ (newSequencer shape)", o.Sequencer)
	}
}
