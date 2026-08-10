package antivirus

import (
	"context"
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

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestSignatureScannerEICAR(t *testing.T) {
	s := NewSignatureScanner(nil)
	if res, _ := s.Scan(context.Background(), strings.NewReader("perfectly safe content")); !res.Clean {
		t.Fatalf("clean content flagged: %+v", res)
	}
	res, err := s.Scan(context.Background(), strings.NewReader("prefix "+EICAR+" suffix"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Clean || res.Signature != "EICAR-Test-File" {
		t.Fatalf("EICAR not detected: %+v", res)
	}
}

func TestSignatureScannerExtra(t *testing.T) {
	s := NewSignatureScanner(map[string]string{"Custom-Mal": "BADBADBAD"})
	res, _ := s.Scan(context.Background(), strings.NewReader("xx BADBADBAD xx"))
	if res.Clean || res.Signature != "Custom-Mal" {
		t.Fatalf("custom signature not detected: %+v", res)
	}
}

func TestHTTPScanner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "virus") {
			_, _ = w.Write([]byte(`{"clean":false,"signature":"Trojan.Test"}`))
			return
		}
		_, _ = w.Write([]byte(`{"clean":true}`))
	}))
	defer srv.Close()
	sc := NewHTTPScanner(srv.URL, "")
	if res, _ := sc.Scan(context.Background(), strings.NewReader("ok")); !res.Clean {
		t.Fatalf("expected clean")
	}
	res, _ := sc.Scan(context.Background(), strings.NewReader("a virus here"))
	if res.Clean || res.Signature != "Trojan.Test" {
		t.Fatalf("expected infected, got %+v", res)
	}
}

func setupSvc(t *testing.T) (repository.Repository, *service.FileService) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "av.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	t.Cleanup(func() { _ = repo.Close() })
	return repo, service.NewFileService(store, repo, nil)
}

func TestWorkerScanCleanTagsObject(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "clean.txt", strings.NewReader("totally fine"), 12, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got, err := repo.GetObject(ctx, "default", "default", "clean.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tags[TagStatus] != "clean" {
		t.Fatalf("expected av_status=clean, got %v", got.Tags)
	}
}

func TestWorkerQuarantinesInfected(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "bad.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	// Quarantine soft-deletes the object.
	if _, err := repo.GetObject(ctx, "default", "default", "bad.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("infected object should be quarantined, got err=%v", err)
	}
	quota, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.UsedBytes != 0 || quota.UsedObjects != 0 {
		t.Fatalf("quarantine left usage at %d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}
}

func TestWorkerNoQuarantineKeepsButTags(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, _ := svc.Put(ctx, "default", "default", "bad2.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, false, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got, err := repo.GetObject(ctx, "default", "default", "bad2.txt")
	if err != nil {
		t.Fatalf("object should remain (no quarantine): %v", err)
	}
	if got.Tags[TagStatus] != "infected" || got.Tags[TagSignature] != "EICAR-Test-File" {
		t.Fatalf("expected infected tags, got %v", got.Tags)
	}
}

func TestWorkerQuarantinesOnlyInfectedVersion(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	if err := svc.SetBucketVersioning(ctx, "default", "default", true); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
	infected, err := svc.Put(ctx, "", "", "versioned.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put infected version: %v", err)
	}
	cleanBody := "replacement"
	current, err := svc.Put(ctx, "", "", "versioned.txt", strings.NewReader(cleanBody), int64(len(cleanBody)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put clean version: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, infected.ID); err != nil {
		t.Fatalf("scan old version: %v", err)
	}
	if _, err := repo.GetObjectByID(ctx, infected.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("infected historical version still exists: %v", err)
	}
	got, err := repo.GetObjectByID(ctx, current.ID)
	if err != nil {
		t.Fatalf("current version removed: %v", err)
	}
	if got.Tags[TagStatus] != "" {
		t.Fatalf("current version received stale antivirus tag: %v", got.Tags)
	}
	quota, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.UsedBytes != int64(len(cleanBody)) || quota.UsedObjects != 1 {
		t.Fatalf("usage = %d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}
}

func TestSignatureScannerName(t *testing.T) {
	s := NewSignatureScanner(nil)
	if s.Name() != "signature" {
		t.Fatalf("expected 'signature', got %q", s.Name())
	}
}

func TestEncodeDecodeObjectID(t *testing.T) {
	payload := EncodeObjectID(42)
	if payload == "" {
		t.Fatal("expected non-empty payload")
	}
	id, err := DecodeObjectID(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if id != 42 {
		t.Fatalf("expected 42, got %d", id)
	}
}

func TestDecodeObjectID_Invalid(t *testing.T) {
	if _, err := DecodeObjectID(`{"object_id":0}`); err == nil {
		t.Fatal("expected error for missing object_id")
	}
	if _, err := DecodeObjectID(`not json`); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ── AC-1a: quarantine writes audit + outbox facts atomically ────────────────

func TestScanObjectByIDQuarantineWritesAuditAndOutbox(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "bad.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Object soft-deleted (existing quarantine semantics preserved).
	got, err := repo.GetObjectByID(ctx, obj.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatal("quarantined object must be soft-deleted")
	}
	quota, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.UsedBytes != 0 || quota.UsedObjects != 0 {
		t.Fatalf("quarantine left usage at %d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}

	// audit_log: exactly one row, actor pinned to system:antivirus (FR-4).
	rows, err := repo.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit_log has %d rows, want 1: %+v", len(rows), rows)
	}
	entry := rows[0]
	if entry.Actor != SystemActor || entry.Action != repository.AuditActionFileDelete ||
		entry.TenantID != "default" || entry.Target != "default/bad.txt" || !strings.Contains(entry.Detail, "av_infected") {
		t.Errorf("audit row = %+v, want actor=%s action=%s detail=av_infected", entry, SystemActor, repository.AuditActionFileDelete)
	}

	// event_outbox: exactly two due facts (deleted@1.1 + notify@1.1), payloads
	// self-contained with reason and signature (FR-2/FR-3).
	facts := claimDueFacts(t, repo)
	if len(facts) != 2 {
		t.Fatalf("event_outbox has %d due facts, want 2", len(facts))
	}
	var deleted, notify *repository.EventOutboxRow
	for i := range facts {
		switch facts[i].EventType {
		case repository.EventTypeFileDeleted11:
			deleted = &facts[i]
		case repository.EventTypeFileNotify11:
			notify = &facts[i]
		}
	}
	if deleted == nil || notify == nil {
		t.Fatalf("expected both fact types, got %+v", facts)
	}
	if deleted.OriginID != obj.ID || notify.OriginID != obj.ID {
		t.Errorf("fact origin_id mismatch: deleted=%d notify=%d want %d", deleted.OriginID, notify.OriginID, obj.ID)
	}
	if !strings.Contains(string(deleted.Payload), `"actor":"system:antivirus"`) ||
		!strings.Contains(string(deleted.Payload), `"reason":"av_infected"`) ||
		!strings.Contains(string(deleted.Payload), `"schema_version":"1.1"`) {
		t.Errorf("deleted@1.1 payload missing actor/reason/version: %s", deleted.Payload)
	}
	if !strings.Contains(string(notify.Payload), `"signature":"EICAR-Test-File"`) ||
		!strings.Contains(string(notify.Payload), `"schema_version":"1.1"`) {
		t.Errorf("notify@1.1 payload missing signature/version: %s", notify.Payload)
	}
}

// ── AC-2: durable_async — dispatcher-stopped job, relay drain is disjoint ──

// TestQuarantineJobCompletesWithoutRelayThenRelayDrainsDisjoint runs a real
// job Pool with no relay (AC-2a): the virus_scan job completes and the outbox
// facts stay pending. It then starts the always-on relay (AC-2b): the facts
// drain to terminal state without touching the completed job or the audit
// log (L0 authoritative). Zero network: no notification rules → relay
// completes silently.
func TestQuarantineJobCompletesWithoutRelayThenRelayDrainsDisjoint(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, quietLogger()).
		WithObjectController(svc)
	queue, pool := newScanPool(t, w)
	poolCtx, cancelPool := context.WithCancel(ctx)
	defer cancelPool()
	go pool.Run(poolCtx)

	// Phase A (AC-2a): job completes with the relay never started.
	objA, err := svc.Put(ctx, "default", "default", "a.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	enqueueScan(t, queue, objA.ID)
	jobA := waitForJobDone(t, repo, objA.ID, 1)
	if jobA.Attempts != 1 {
		t.Fatalf("job attempts = %d, want 1 (no retry storm)", jobA.Attempts)
	}
	// Facts are still due — durable despite no dispatcher (FR-5).
	facts := claimDueFacts(t, repo)
	if len(facts) != 2 {
		t.Fatalf("phase A: event_outbox has %d due facts, want 2 (durable)", len(facts))
	}
	for _, fact := range facts {
		if fact.OriginID != objA.ID || !strings.Contains(string(fact.Payload), `"actor":"system:antivirus"`) {
			t.Errorf("phase A fact = %+v, want origin %d with pinned actor", fact, objA.ID)
		}
	}
	// Tidy: complete the claimed rows so phase B starts from a clean slate.
	for _, fact := range facts {
		if err := repo.CompleteEventOutbox(ctx, fact.ID, claimOwner, claimToken); err != nil {
			t.Fatalf("complete phase A fact: %v", err)
		}
	}

	// Phase B (AC-2b): relay drains a second quarantine without reentering jobs.
	objB, err := svc.Put(ctx, "default", "default", "b.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	enqueueScan(t, queue, objB.ID)
	jobB := waitForJobDone(t, repo, objB.ID, 2)
	if jobB.Attempts != 1 {
		t.Fatalf("jobB attempts = %d, want 1", jobB.Attempts)
	}
	auditBefore, err := repo.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}

	drainOutboxWithRelay(t, repo)

	// Nothing due remains: both rows reached terminal state (delivered — zero
	// network, no rules; sink nil → complete).
	if leftover := claimDueFacts(t, repo); len(leftover) != 0 {
		t.Fatalf("relay left %d due facts, want 0 (terminal states)", len(leftover))
	}
	// Relay drain never re-enters business jobs: no new virus_scan rows, the
	// completed jobs are untouched, and the audit log is unchanged (L0).
	jobs, err := repo.ListJobs(ctx, "", JobScan, 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs table has %d virus_scan rows, want 2 (no reentry)", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != repository.JobSucceeded || j.Attempts != 1 {
			t.Errorf("job after relay drain = %+v, want succeeded attempts=1", j)
		}
	}
	auditAfter, err := repo.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(auditAfter) != len(auditBefore) {
		t.Fatalf("audit rows changed by relay drain: %d → %d", len(auditBefore), len(auditAfter))
	}
}

// ── AC-3a: quarantine golden bytes (fixed inputs, byte-exact) ───────────────

const goldenQuarantineDeletedFact = `{"schema_version":"1.1","event_type":"vault.file.deleted@1.1","tenant":"default","bucket":"default","key":"docs/a.txt","object_id":42,"version_id":"v-abc","size":42,"etag":"etag-1","backend":"local","request_id":"req-av-1","actor":"system:antivirus","reason":"av_infected"}`

const goldenQuarantineNotifyFact = `{"schema_version":"1.1","event_type":"vault.file.notify@1.1","tenant":"default","bucket":"default","key":"docs/a.txt","version_id":"v-abc","size":42,"etag":"etag-1","backend":"local","request_id":"req-av-1","actor":"system:antivirus","records":[{"eventVersion":"2.1","eventSource":"aws:s3","awsRegion":"us-east-1","eventName":"s3:ObjectRemoved:Delete","userIdentity":{"principalId":"default"},"s3":{"s3SchemaVersion":"1.0","bucket":{"name":"default","arn":"arn:aws:s3:::default"},"object":{"key":"docs/a.txt","size":42,"eTag":"etag-1","versionId":"v-abc","sequencer":"8f3a2c9e4b1d7f06a5c0e2d94b7a1f3e"}}}],"signature":"EICAR-Test-File"}`

// TestQuarantineFactGoldenBytes pins the quarantine-path payloads byte-exact
// with the production builders (the same calls quarantineFacts makes): the
// deleted@1.1 fact carries reason=av_infected after actor; the notify@1.1
// fact carries the AV signature after records; schema stays 1.1. The REST-path
// goldens in events/schema_test.go remain untouched (AC-3b).
func TestQuarantineFactGoldenBytes(t *testing.T) {
	obj := repository.Object{
		ID:        42, // pinned: prevents a silent "object_id":0 in the golden bytes
		Bucket:    "default",
		Key:       "docs/a.txt",
		VersionID: "v-abc",
		Size:      42,
		ETag:      "etag-1",
		Backend:   "local",
	}
	deleted := string(events.BuildDeletedFact(obj, SystemActor, "req-av-1", "default", "av_infected"))
	if deleted != goldenQuarantineDeletedFact {
		t.Errorf("quarantine deleted@1.1 golden mismatch\n got: %s\nwant: %s", deleted, goldenQuarantineDeletedFact)
	}
	notify := string(events.BuildNotifyFact(obj, SystemActor, "req-av-1", "default", "8f3a2c9e4b1d7f06a5c0e2d94b7a1f3e", "EICAR-Test-File"))
	if notify != goldenQuarantineNotifyFact {
		t.Errorf("quarantine notify@1.1 golden mismatch\n got: %s\nwant: %s", notify, goldenQuarantineNotifyFact)
	}
}

// ── AC-4: composition e2e — EICAR → job → quarantine → poll + idempotency ──

func TestQuarantineCompositionE2E(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, quietLogger()).
		WithObjectController(svc)
	queue, pool := newScanPool(t, w)
	poolCtx, cancelPool := context.WithCancel(ctx)
	defer cancelPool()
	go pool.Run(poolCtx)

	obj, err := svc.Put(ctx, "default", "default", "e2e.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	enqueueScan(t, queue, obj.ID)
	// Dedupe key: a duplicate enqueue short-circuits (no double job).
	if _, deduped, err := queue.Enqueue(ctx, repository.Job{
		TenantID:  "default",
		Type:      JobScan,
		Payload:   EncodeObjectID(obj.ID),
		DedupeKey: fmt.Sprintf("%s:%d", JobScan, obj.ID),
	}); err != nil || !deduped {
		t.Fatalf("duplicate enqueue: deduped=%v err=%v, want deduped=true", deduped, err)
	}

	job := waitForJobDone(t, repo, obj.ID, 1)
	if job.Attempts != 1 {
		t.Fatalf("job attempts = %d, want 1", job.Attempts)
	}

	// audit_log: exactly 1 quarantine row.
	rows, err := repo.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(rows) != 1 || rows[0].Actor != SystemActor || !strings.Contains(rows[0].Detail, "av_infected") {
		t.Fatalf("audit rows = %+v, want exactly 1 system:antivirus quarantine row", rows)
	}

	// event_outbox: exactly 2 due facts with self-contained payloads.
	facts := claimDueFacts(t, repo)
	if len(facts) != 2 {
		t.Fatalf("event_outbox has %d due facts, want 2", len(facts))
	}
	var sawDeleted, sawNotify bool
	for _, fact := range facts {
		if fact.OriginID != obj.ID {
			t.Errorf("fact origin = %d, want %d", fact.OriginID, obj.ID)
		}
		switch fact.EventType {
		case repository.EventTypeFileDeleted11:
			sawDeleted = strings.Contains(string(fact.Payload), `"reason":"av_infected"`)
		case repository.EventTypeFileNotify11:
			sawNotify = strings.Contains(string(fact.Payload), `"signature":"EICAR-Test-File"`)
		}
	}
	if !sawDeleted || !sawNotify {
		t.Errorf("payload content: deletedHasReason=%v notifyHasSignature=%v", sawDeleted, sawNotify)
	}

	// Repeat delivery (job retry simulation): the DeletedAt guard makes the
	// second handler run a no-op — outbox/audit counts unchanged (idempotent;
	// no UNIQUE(event_type, origin_id) needed, C-7).
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("repeat delivery: %v", err)
	}
	rows, err = repo.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("repeat delivery added %d audit rows, want 0", len(rows)-1)
	}
	// The two original facts are still the only ones (claimed above; a second
	// write would have produced 4 due rows before claim — here we assert the
	// claim already saw exactly 2 and nothing new is due after re-delivery).
	if extra := claimDueFacts(t, repo); len(extra) != 0 {
		t.Fatalf("repeat delivery produced %d new facts, want 0", len(extra))
	}

	// Relay drain: terminal states, audit untouched.
	drainOutboxWithRelay(t, repo)
	rows, err = repo.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("relay drain changed audit count to %d, want 1", len(rows))
	}
}

// ── Security hardening: tenant consistency + signature bound ────────────────

// TestScanObjectByIDRejectsTenantMismatch fails closed when a job's tenant
// does not match the object's tenant: the unscoped GetObjectByID plus the
// system bypass would otherwise make the quarantine path a cross-tenant
// primitive for any future job-create surface.
func TestScanObjectByIDRejectsTenantMismatch(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "mismatch.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, quietLogger()).
		WithObjectController(svc)
	err = w.ScanObjectByID(access.SystemContext(ctx, "other-tenant"), obj.ID)
	if err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("tenant mismatch error = %v, want tenant error", err)
	}
	got, err := repo.GetObject(ctx, "default", "default", "mismatch.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DeletedAt != nil || got.Tags[TagStatus] != "" {
		t.Fatalf("foreign-tenant job mutated the object: deleted_at=%v tags=%v", got.DeletedAt, got.Tags)
	}
	if leftover := claimDueFacts(t, repo); len(leftover) != 0 {
		t.Fatalf("foreign-tenant job wrote %d facts, want 0", len(leftover))
	}
}

// TestScanObjectByIDBoundsOversizedSignature caps a misbehaving HTTP scanner's
// signature before the quarantine transaction so quarantine cannot be wedged
// into permanent validation rollback (availability guard).
func TestScanObjectByIDBoundsOversizedSignature(t *testing.T) {
	ctx := context.Background()
	bigSig := strings.Repeat("x", 8192)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clean":false,"signature":"` + bigSig + `"}`))
	}))
	defer srv.Close()

	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "big.txt", strings.NewReader("payload"), 7, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewHTTPScanner(srv.URL, ""), nil, true, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("oversized signature must not fail the scan: %v", err)
	}
	got, err := repo.GetObjectByID(ctx, obj.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatal("infected object with oversized signature must still quarantine")
	}
	if len(got.Tags[TagSignature]) != maxSignatureBytes {
		t.Fatalf("av_signature tag length = %d, want %d (capped)", len(got.Tags[TagSignature]), maxSignatureBytes)
	}
	facts := claimDueFacts(t, repo)
	if len(facts) != 2 {
		t.Fatalf("event_outbox has %d facts, want 2 (quarantine committed)", len(facts))
	}
	capped := `"signature":"` + strings.Repeat("x", maxSignatureBytes) + `"`
	var sawNotify bool
	for _, fact := range facts {
		if fact.EventType == repository.EventTypeFileNotify11 {
			sawNotify = strings.Contains(string(fact.Payload), capped)
		}
	}
	if !sawNotify {
		t.Error("notify@1.1 payload must carry the truncated signature (passes the 1 MiB gate)")
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const (
	claimOwner = "test-owner"
	claimToken = "test-token"
)

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func claimDueFacts(t *testing.T, repo repository.Repository) []repository.EventOutboxRow {
	t.Helper()
	facts, err := repo.ClaimEventOutbox(context.Background(), claimOwner, claimToken, 100, time.Minute)
	if err != nil {
		t.Fatalf("claim event outbox: %v", err)
	}
	return facts
}

// newScanPool wires the production-shaped job pipeline: queue + registry with
// the ScanObjectByID handler under SystemContext (mirroring cmd/server), and
// a single-worker Pool. Returns the queue for enqueueing; the pool must be
// started by the caller (go pool.Run(ctx)).
func newScanPool(t *testing.T, w *Worker) (*jobs.Queue, *jobs.Pool) {
	t.Helper()
	reg := jobs.NewRegistry()
	reg.Register(JobScan, func(ctx context.Context, job repository.Job) error {
		id, err := DecodeObjectID(job.Payload)
		if err != nil {
			return err
		}
		return w.ScanObjectByID(access.SystemContext(ctx, job.TenantID), id)
	})
	return jobs.NewQueue(w.repo), jobs.NewPool(w.repo, reg, 1, quietLogger())
}

func enqueueScan(t *testing.T, queue *jobs.Queue, objectID int64) {
	t.Helper()
	if _, _, err := queue.Enqueue(context.Background(), repository.Job{
		TenantID:  "default",
		Type:      JobScan,
		Payload:   EncodeObjectID(objectID),
		DedupeKey: fmt.Sprintf("%s:%d", JobScan, objectID),
	}); err != nil {
		t.Fatalf("enqueue virus_scan: %v", err)
	}
}

// waitForJobDone polls the jobs table until the virus_scan job for objectID is
// succeeded, expecting exactly wantJobs total virus_scan rows. Returns the job.
func waitForJobDone(t *testing.T, repo repository.Repository, objectID int64, wantJobs int) repository.Job {
	t.Helper()
	var job repository.Job
	waitFor(t, 8*time.Second, func() bool {
		jobs, err := repo.ListJobs(context.Background(), "", JobScan, 100)
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if len(jobs) != wantJobs {
			return false
		}
		for _, j := range jobs {
			if j.Status != repository.JobSucceeded {
				return false
			}
		}
		job = jobs[0]
		return true
	})
	return job
}

// drainOutboxWithRelay runs the always-on relay long enough to drain every
// due fact (no notification rules + nil L2 sink → complete, zero network),
// then stops it. After return, ClaimEventOutbox must return nothing.
func drainOutboxWithRelay(t *testing.T, repo repository.Repository) {
	t.Helper()
	relay := events.NewEventOutboxRelay(repo, quietLogger(), events.EventOutboxRelayOptions{
		PollInterval: 10 * time.Millisecond,
		ClaimTTL:     500 * time.Millisecond,
		BatchSize:    100,
	})
	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Run(relayCtx)
	}()
	time.Sleep(300 * time.Millisecond) // first batch fires immediately; drain happens well within this window
	cancel()
	<-done
}
