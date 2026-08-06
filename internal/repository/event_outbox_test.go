package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// openEventOutboxTestStore opens a fresh SQLite store with the 0041 migration
// applied (Migrate auto-applies every embedded .up file).
func openEventOutboxTestStore(t *testing.T) Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

func seedOutboxObject(t *testing.T, repo Repository, tenant, bucket, key string) Object {
	t.Helper()
	obj, err := repo.UpsertObject(context.Background(), Object{
		TenantID:    tenant,
		Bucket:      bucket,
		Key:         key,
		VersionID:   "v-1",
		Backend:     "local",
		StorageKey:  tenant + "/" + bucket + "/" + key + "@v-1",
		Size:        42,
		ETag:        "etag-1",
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("seed object: %v", err)
	}
	if obj.ID <= 0 {
		t.Fatalf("seeded object has no id: %+v", obj)
	}
	return obj
}

// validDeleteFacts returns the two facts FileService.Delete writes (FR-2),
// with the AC-4 object_id present in the deleted@1.1 envelope.
func validDeleteFacts(obj Object, tenant string) []OutboxFact {
	payload := `{"schema_version":"1.1","event_type":"vault.file.deleted@1.1","tenant":"` + tenant +
		`","bucket":"` + obj.Bucket + `","key":"` + obj.Key + `","object_id":` + strconv.FormatInt(obj.ID, 10) +
		`,"version_id":"v-1","size":42,"etag":"etag-1","backend":"local"}`
	notify := `{"schema_version":"1.1","event_type":"vault.file.notify@1.1","tenant":"` + tenant +
		`","bucket":"` + obj.Bucket + `","key":"` + obj.Key + `","version_id":"v-1","size":42,"etag":"etag-1","backend":"local"}`
	return []OutboxFact{
		{EventType: EventTypeFileDeleted11, OriginID: obj.ID, TenantID: tenant, Payload: []byte(payload)},
		{EventType: EventTypeFileNotify11, OriginID: obj.ID, TenantID: tenant, Payload: []byte(notify)},
	}
}

// ── AC-1: one-transaction atomicity ─────────────────────────────────────────

// TestDeleteObjectWithAudit_OneTx extends TestDeleteObjectWithEvent_OneTx with
// the FR-1 audit row: the delete transaction = metadata delete + audit_log
// row + outbox facts, committed together and rolled back together.
func TestDeleteObjectWithAudit_OneTx(t *testing.T) {
	ctx := context.Background()
	entry := AuditEntry{Actor: "alice", Action: AuditActionFileDelete, Target: "b1/k1", TenantID: "t1", Detail: "hard"}

	t.Run("hard delete commits audit row and facts together", func(t *testing.T) {
		repo := openEventOutboxTestStore(t)
		obj := seedOutboxObject(t, repo, "t1", "b1", "k1")
		if err := repo.HardDeleteObjectWithEvent(ctx, "t1", "b1", "k1", entry, validDeleteFacts(obj, "t1")); err != nil {
			t.Fatalf("hard delete with event: %v", err)
		}
		if _, err := repo.GetObject(ctx, "t1", "b1", "k1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetObject after hard delete = %v, want ErrNotFound", err)
		}
		assertOutboxRows(t, repo, 2)
		assertDeletedFactPayload(t, repo, obj)
		assertAuditRows(t, repo, entry)
	})

	t.Run("soft delete commits audit row and facts together", func(t *testing.T) {
		repo := openEventOutboxTestStore(t)
		obj := seedOutboxObject(t, repo, "t3", "b3", "k3")
		soft := AuditEntry{Actor: "bob", Action: AuditActionFileDelete, Target: "b3/k3", TenantID: "t3", Detail: "soft"}
		if err := repo.SoftDeleteObjectWithEvent(ctx, "t3", "b3", "k3", soft, validDeleteFacts(obj, "t3")); err != nil {
			t.Fatalf("soft delete with event: %v", err)
		}
		assertOutboxRows(t, repo, 2)
		assertAuditRows(t, repo, soft)
	})

	t.Run("forced rollback: invalid fact leaves object, outbox, and audit untouched", func(t *testing.T) {
		repo := openEventOutboxTestStore(t)
		obj := seedOutboxObject(t, repo, "t2", "b2", "k2")
		bad := []OutboxFact{
			{EventType: "vault.file.bogus@9.9", OriginID: obj.ID, TenantID: "t2", Payload: []byte(`{"schema_version":"1.1"}`)},
		}
		err := repo.HardDeleteObjectWithEvent(ctx, "t2", "b2", "k2", entry, bad)
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if _, err := repo.GetObject(ctx, "t2", "b2", "k2"); err != nil {
			t.Fatalf("object must survive rollback, got %v", err)
		}
		assertOutboxRows(t, repo, 0)
		assertAuditRows(t, repo, AuditEntry{}) // zero rows
	})

	t.Run("oversized payload rolls back with the delete (F9/G4)", func(t *testing.T) {
		repo := openEventOutboxTestStore(t)
		obj := seedOutboxObject(t, repo, "t5", "b5", "k5")
		oversized := []OutboxFact{
			{EventType: EventTypeFileDeleted11, OriginID: obj.ID, TenantID: "t5",
				Payload: append([]byte(`{"schema_version":"1.1","pad":"`), bytes.Repeat([]byte("x"), maxOutboxFactPayloadBytes)...)},
		}
		err := repo.HardDeleteObjectWithEvent(ctx, "t5", "b5", "k5", entry, oversized)
		if err == nil {
			t.Fatal("expected payload size error, got nil")
		}
		if _, err := repo.GetObject(ctx, "t5", "b5", "k5"); err != nil {
			t.Fatalf("object must survive rollback, got %v", err)
		}
		assertOutboxRows(t, repo, 0)
		assertAuditRows(t, repo, AuditEntry{})
	})
}

func TestDeleteObjectWithEvent_OneTx(t *testing.T) {
	ctx := context.Background()

	t.Run("hard delete commits rows and facts together", func(t *testing.T) {
		repo := openEventOutboxTestStore(t)
		obj := seedOutboxObject(t, repo, "t1", "b1", "k1")
		if err := repo.HardDeleteObjectWithEvent(ctx, "t1", "b1", "k1", AuditEntry{}, validDeleteFacts(obj, "t1")); err != nil {
			t.Fatalf("hard delete with event: %v", err)
		}
		if _, err := repo.GetObject(ctx, "t1", "b1", "k1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetObject after hard delete = %v, want ErrNotFound", err)
		}
		assertOutboxRows(t, repo, 2)
		rows, err := listEventOutbox(t, repo)
		if err != nil {
			t.Fatal(err)
		}
		types := map[OutboxEventType]bool{}
		for _, row := range rows {
			types[row.EventType] = true
			if row.OriginID != obj.ID {
				t.Errorf("fact origin_id = %d, want %d", row.OriginID, obj.ID)
			}
			if row.TenantID != "t1" {
				t.Errorf("fact tenant = %q", row.TenantID)
			}
			if row.Status != "pending" {
				t.Errorf("fact status = %q, want pending", row.Status)
			}
		}
		if !types[EventTypeFileDeleted11] || !types[EventTypeFileNotify11] {
			t.Errorf("expected both fact types, got %v", types)
		}
		assertDeliveredRows(t, repo, 0)
	})

	t.Run("forced rollback: invalid fact leaves object and outbox untouched", func(t *testing.T) {
		repo := openEventOutboxTestStore(t)
		obj := seedOutboxObject(t, repo, "t2", "b2", "k2")
		bad := []OutboxFact{
			{EventType: "vault.file.bogus@9.9", OriginID: obj.ID, TenantID: "t2", Payload: []byte(`{"schema_version":"1.1"}`)},
		}
		err := repo.HardDeleteObjectWithEvent(ctx, "t2", "b2", "k2", AuditEntry{}, bad)
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if _, err := repo.GetObject(ctx, "t2", "b2", "k2"); err != nil {
			t.Fatalf("object must survive rollback, got %v", err)
		}
		assertOutboxRows(t, repo, 0)
	})

	t.Run("soft delete commits deleted_at and facts together", func(t *testing.T) {
		repo := openEventOutboxTestStore(t)
		obj := seedOutboxObject(t, repo, "t3", "b3", "k3")
		if err := repo.SoftDeleteObjectWithEvent(ctx, "t3", "b3", "k3", AuditEntry{}, validDeleteFacts(obj, "t3")); err != nil {
			t.Fatalf("soft delete with event: %v", err)
		}
		got, err := repo.GetObjectByID(ctx, obj.ID)
		if err != nil {
			t.Fatalf("soft-deleted row must remain readable by id: %v", err)
		}
		if got.DeletedAt == nil {
			t.Fatal("deleted_at not set")
		}
		assertOutboxRows(t, repo, 2)
	})

	t.Run("no-op delete leaves no phantom facts", func(t *testing.T) {
		repo := openEventOutboxTestStore(t)
		// Object never seeded: HardDeleteObjectWithEvent deletes 0 rows and must
		// roll back with ErrNotFound instead of committing phantom facts (GAP-4).
		err := repo.HardDeleteObjectWithEvent(ctx, "t4", "b4", "missing", AuditEntry{}, []OutboxFact{
			{EventType: EventTypeFileDeleted11, OriginID: 999, TenantID: "t4", Payload: []byte(`{"schema_version":"1.1"}`)},
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("no-op delete = %v, want ErrNotFound", err)
		}
		assertOutboxRows(t, repo, 0)
	})
}

// ── AC-2(1/4): claim → complete lifecycle, exactly-once post-complete ───────

func TestEventOutboxClaimCompleteLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := openEventOutboxTestStore(t)
	obj := seedOutboxObject(t, repo, "t1", "b1", "k1")
	if err := repo.HardDeleteObjectWithEvent(ctx, "t1", "b1", "k1", AuditEntry{}, validDeleteFacts(obj, "t1")); err != nil {
		t.Fatal(err)
	}

	claimed, err := repo.ClaimEventOutbox(ctx, "owner-a", "token-a", 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d facts, want 2", len(claimed))
	}
	for _, fact := range claimed {
		if fact.ClaimOwner != "owner-a" || fact.ClaimToken != "token-a" {
			t.Errorf("claim fencing not applied: %+v", fact)
		}
		if fact.Attempts != 1 {
			t.Errorf("attempts = %d, want 1", fact.Attempts)
		}
		if !fact.LeaseExpiresAt.After(time.Now()) {
			t.Errorf("lease not set: %v", fact.LeaseExpiresAt)
		}
		if err := repo.CompleteEventOutbox(ctx, fact.ID, "owner-a", "token-a"); err != nil {
			t.Fatalf("complete %d: %v", fact.ID, err)
		}
	}

	// Exactly-once post-complete: nothing due remains (authoritative status).
	if pending, err := repo.ClaimEventOutbox(ctx, "owner-b", "token-b", 10, time.Minute); err != nil || len(pending) != 0 {
		t.Fatalf("delivered facts reclaimed: len=%d err=%v", len(pending), err)
	}
	assertDeliveredRows(t, repo, 2)
}

// ── AC-2(3): lease-expiry redelivery after simulated crash ──────────────────

func TestEventOutboxClaimLeaseExpiryRedelivers(t *testing.T) {
	ctx := context.Background()
	repo := openEventOutboxTestStore(t)
	obj := seedOutboxObject(t, repo, "t1", "b1", "k1")
	if err := repo.HardDeleteObjectWithEvent(ctx, "t1", "b1", "k1", AuditEntry{}, validDeleteFacts(obj, "t1")); err != nil {
		t.Fatal(err)
	}

	// owner-a claims with a short lease and "crashes" (never completes).
	claimed, err := repo.ClaimEventOutbox(ctx, "owner-a", "token-a", 10, 10*time.Millisecond)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("first claim: len=%d err=%v", len(claimed), err)
	}

	// Wait for the real condition: the lease deadline passing.
	time.Sleep(time.Until(claimed[0].LeaseExpiresAt) + 10*time.Millisecond)

	// owner-b reclaims the same facts after the lease expired (crash redelivery).
	redelivered, err := repo.ClaimEventOutbox(ctx, "owner-b", "token-b", 10, time.Minute)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(redelivered) != 2 {
		t.Fatalf("reclaimed %d facts, want 2 (lease-expired redelivery)", len(redelivered))
	}
	for _, fact := range redelivered {
		if fact.ClaimOwner != "owner-b" {
			t.Errorf("reclaimed fact owner = %q, want owner-b", fact.ClaimOwner)
		}
		// owner-a's token must no longer complete the fact (fencing).
		if err := repo.CompleteEventOutbox(ctx, fact.ID, "owner-a", "token-a"); err == nil {
			t.Error("stale owner-a complete succeeded; fencing broken")
		}
		if err := repo.CompleteEventOutbox(ctx, fact.ID, "owner-b", "token-b"); err != nil {
			t.Fatalf("owner-b complete: %v", err)
		}
	}
}

// ── AC-2(2): retry with backoff and terminal failed state ───────────────────

func TestEventOutboxRetryBackoffAndTerminalFailed(t *testing.T) {
	ctx := context.Background()
	repo := openEventOutboxTestStore(t)
	obj := seedOutboxObject(t, repo, "t1", "b1", "k1")
	if err := repo.HardDeleteObjectWithEvent(ctx, "t1", "b1", "k1", AuditEntry{}, validDeleteFacts(obj, "t1")); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimEventOutbox(ctx, "owner", "token", 10, time.Minute)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim: len=%d err=%v", len(claimed), err)
	}
	// Backoff gate: after retry the fact must not be due before its backoff.
	for _, fact := range claimed {
		if err := repo.RetryEventOutbox(ctx, fact.ID, "owner", "token", "500 boom",
			time.Now().UTC().Add(time.Second), 2); err != nil {
			t.Fatalf("retry: %v", err)
		}
	}
	if due, err := repo.ClaimEventOutbox(ctx, "owner", "token-2", 10, time.Minute); err != nil || len(due) != 0 {
		t.Fatalf("not-yet-due facts claimed: len=%d err=%v", len(due), err)
	}

	// Fast-forward available_at_ns, reclaim (attempts=2), then exhaust the
	// maxAttempts=2 budget: attempts >= max → terminal 'failed'.
	if err := backdateEventOutbox(t, repo, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	due, err := repo.ClaimEventOutbox(ctx, "owner", "token-3", 10, time.Minute)
	if err != nil || len(due) != 2 {
		t.Fatalf("due reclaim: len=%d err=%v", len(due), err)
	}
	for _, fact := range due {
		if err := repo.RetryEventOutbox(ctx, fact.ID, "owner", "token-3", "still failing",
			time.Now().UTC().Add(time.Second), 2); err != nil {
			t.Fatalf("terminal retry: %v", err)
		}
	}
	statuses, err := eventOutboxStatuses(t, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status != "failed" {
			t.Errorf("status = %q, want failed", status)
		}
	}

	// Terminal failed rows are never claimable again.
	if again, err := repo.ClaimEventOutbox(ctx, "owner", "token-4", 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("failed facts reclaimed: len=%d err=%v", len(again), err)
	}
}

// ── Prune ───────────────────────────────────────────────────────────────────

func TestEventOutboxPrune(t *testing.T) {
	ctx := context.Background()
	repo := openEventOutboxTestStore(t)
	obj := seedOutboxObject(t, repo, "t1", "b1", "k1")
	if err := repo.HardDeleteObjectWithEvent(ctx, "t1", "b1", "k1", AuditEntry{}, validDeleteFacts(obj, "t1")); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimEventOutbox(ctx, "owner", "token", 10, time.Minute)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim: len=%d err=%v", len(claimed), err)
	}
	for _, fact := range claimed {
		if err := repo.CompleteEventOutbox(ctx, fact.ID, "owner", "token"); err != nil {
			t.Fatal(err)
		}
	}

	// Fresh rows are younger than the retention window: prune removes nothing.
	if n, err := repo.PruneEventOutbox(ctx, time.Now().Add(-24*time.Hour), time.Now().Add(-7*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("early prune: n=%d err=%v", n, err)
	}
	assertDeliveredRows(t, repo, 2)

	// Backdate delivery, then prune removes delivered rows + fidelity records.
	if err := backdateEventOutboxDelivered(t, repo); err != nil {
		t.Fatal(err)
	}
	n, err := repo.PruneEventOutbox(ctx, time.Now().Add(-time.Hour), time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 2 {
		t.Fatalf("pruned %d rows, want 2", n)
	}
	assertOutboxRows(t, repo, 0)
	assertDeliveredRows(t, repo, 0)
}

// ── HasEventOutboxFact (D2) ─────────────────────────────────────────────────

func TestHasEventOutboxFact(t *testing.T) {
	ctx := context.Background()
	repo := openEventOutboxTestStore(t)
	obj := seedOutboxObject(t, repo, "t1", "b1", "k1")
	if err := repo.HardDeleteObjectWithEvent(ctx, "t1", "b1", "k1", AuditEntry{}, validDeleteFacts(obj, "t1")); err != nil {
		t.Fatal(err)
	}
	has, err := repo.HasEventOutboxFact(ctx, obj.ID, EventTypeFileNotify11)
	if err != nil || !has {
		t.Fatalf("notify fact visible: has=%v err=%v", has, err)
	}
	// E14 paths have no outbox row: the guard must report false so the bus
	// notifier keeps delivering (D2/GAP-1 regression guard).
	other, err := repo.UpsertObject(ctx, Object{
		TenantID: "t1", Bucket: "b1", Key: "e14.txt", VersionID: "v-9",
		Backend: "local", StorageKey: "t1/b1/e14.txt@v-9", Size: 1, ETag: "e",
	})
	if err != nil {
		t.Fatal(err)
	}
	has, err = repo.HasEventOutboxFact(ctx, other.ID, EventTypeFileNotify11)
	if err != nil || has {
		t.Fatalf("E14 object must not have outbox facts: has=%v err=%v", has, err)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func assertOutboxRows(t *testing.T, repo Repository, want int) {
	t.Helper()
	rows, err := listEventOutbox(t, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != want {
		t.Fatalf("event_outbox has %d rows, want %d", len(rows), want)
	}
}

// assertAuditRows asserts the audit_log table holds exactly the given entry
// (or zero rows when the entry is zero-valued), newest-first. An empty
// entry's ID/fields are ignored for the zero-row case.
func assertAuditRows(t *testing.T, repo Repository, want AuditEntry) {
	t.Helper()
	rows, err := repo.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if want.ID == 0 && want.Actor == "" && want.Action == "" && want.Target == "" && want.TenantID == "" && want.Detail == "" {
		if len(rows) != 0 {
			t.Fatalf("audit_log has %d rows, want 0", len(rows))
		}
		return
	}
	if len(rows) != 1 {
		t.Fatalf("audit_log has %d rows, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.ID <= 0 || got.CreatedAt == "" {
		t.Errorf("audit row missing id/created_at: %+v", got)
	}
	if got.Actor != want.Actor || got.Action != want.Action || got.Target != want.Target ||
		got.TenantID != want.TenantID || got.Detail != want.Detail {
		t.Errorf("audit row = %+v, want %+v", got, want)
	}
}

// assertDeletedFactPayload asserts the single deleted@1.1 outbox row carries a
// payload with schema_version 1.1 and object_id == obj.ID (AC-1/AC-4).
func assertDeletedFactPayload(t *testing.T, repo Repository, obj Object) {
	t.Helper()
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo is %T, want *sqlStore", repo)
	}
	var payload string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT payload FROM event_outbox WHERE event_type=$1`, string(EventTypeFileDeleted11)).Scan(&payload); err != nil {
		t.Fatalf("read deleted@1.1 payload: %v", err)
	}
	var doc struct {
		SchemaVersion string `json:"schema_version"`
		ObjectID      int64  `json:"object_id"`
	}
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if doc.SchemaVersion != "1.1" {
		t.Errorf("schema_version = %q, want 1.1", doc.SchemaVersion)
	}
	if doc.ObjectID != obj.ID {
		t.Errorf("object_id = %d, want %d (obj.ID)", doc.ObjectID, obj.ID)
	}
}

type outboxRowMeta struct {
	ID         int64
	EventType  OutboxEventType
	OriginID   int64
	TenantID   string
	Status     string
	Attempts   int
	ClaimOwner string
}

func listEventOutbox(t *testing.T, repo Repository) ([]outboxRowMeta, error) {
	t.Helper()
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo is %T, want *sqlStore", repo)
	}
	rows, err := store.db.QueryContext(context.Background(),
		`SELECT id,event_type,origin_id,tenant_id,status,attempts,claim_owner FROM event_outbox ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []outboxRowMeta
	for rows.Next() {
		var r outboxRowMeta
		var eventType string
		if err := rows.Scan(&r.ID, &eventType, &r.OriginID, &r.TenantID, &r.Status, &r.Attempts, &r.ClaimOwner); err != nil {
			return nil, err
		}
		r.EventType = OutboxEventType(eventType)
		out = append(out, r)
	}
	return out, rows.Err()
}

func eventOutboxStatuses(t *testing.T, repo Repository) ([]string, error) {
	t.Helper()
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo is %T, want *sqlStore", repo)
	}
	rows, err := store.db.QueryContext(context.Background(), `SELECT status FROM event_outbox ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func backdateEventOutbox(t *testing.T, repo Repository, when time.Time) error {
	t.Helper()
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo is %T, want *sqlStore", repo)
	}
	_, err := store.db.ExecContext(context.Background(),
		`UPDATE event_outbox SET available_at_ns=$1 WHERE status='pending'`, when.UnixNano())
	return err
}

func backdateEventOutboxDelivered(t *testing.T, repo Repository) error {
	t.Helper()
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo is %T, want *sqlStore", repo)
	}
	_, err := store.db.ExecContext(context.Background(),
		`UPDATE event_outbox SET delivered_at_ns=$1 WHERE status='delivered'`, time.Now().Add(-2*time.Hour).UnixNano())
	return err
}

func assertDeliveredRows(t *testing.T, repo Repository, want int) {
	t.Helper()
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo is %T, want *sqlStore", repo)
	}
	var n int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM event_outbox_delivered`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("event_outbox_delivered has %d rows, want %d", n, want)
	}
}
