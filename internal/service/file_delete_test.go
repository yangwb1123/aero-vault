package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestFileServiceDelete_WritesAuditRow — AC-1 service level: FileService.Delete
// commits exactly one audit_log row per delete (hard and soft), atomically
// with the deleted@1.1 outbox fact, with the actor from the access principal
// (empty when no principal is in context — no new identity pipeline).
func TestFileServiceDelete_WritesAuditRow(t *testing.T) {
	t.Run("hard delete writes file.delete audit row with actor", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		ctx := access.WithPrincipal(context.Background(), access.Principal{SubjectID: "alice"})
		obj := putTestObject(t, svc, "audit-hard.txt", "body")
		if err := svc.Delete(ctx, "", "", obj.Key, true); err != nil {
			t.Fatalf("hard delete: %v", err)
		}
		assertDeleteAuditRow(t, repo, "alice", "hard", "default", obj)
		assertDeletedFactVisible(t, repo, obj.ID)
	})

	t.Run("soft delete writes file.delete audit row", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		ctx := context.Background() // no principal → empty actor is legal
		obj := putTestObject(t, svc, "audit-soft.txt", "body")
		if err := svc.Delete(ctx, "", "", obj.Key, false); err != nil {
			t.Fatalf("soft delete: %v", err)
		}
		assertDeleteAuditRow(t, repo, "", "soft", "default", obj)
		assertDeletedFactVisible(t, repo, obj.ID)
	})

	t.Run("delete of missing object writes nothing", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		if err := svc.Delete(context.Background(), "", "", "missing.txt", true); err == nil {
			t.Fatal("expected ErrNotFound")
		}
		rows, err := repo.ListAudit(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("audit rows = %d, want 0", len(rows))
		}
	})
}

// denyAuthorizer is a stub Authorizer that denies every request with a fixed
// reason — the fail-closed shape the CLI direction renders (AC-2/AC-3).
type denyAuthorizer struct{ reason string }

func (d denyAuthorizer) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: false, Reason: d.reason}, nil
}

// TestDeleteDenied_NoOutboxRow_ObjectUntouched — AC-2/AC-3 (T6/T10): a denied
// delete returns ErrForbidden before any write path: zero outbox facts, zero
// event-bus broadcast, zero audit rows, and the object stays intact.
func TestDeleteDenied_NoOutboxRow_ObjectUntouched(t *testing.T) {
	svc, repo := newTestSvc(t)
	ctx := context.Background()
	obj := putTestObject(t, svc, "denied.txt", "body")

	bus := events.New(repo, nil)
	svc.WithEventSink(bus)
	ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()

	svc.WithAuthorizer(denyAuthorizer{reason: "default_deny"})
	if err := svc.Delete(ctx, "", "", obj.Key, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Delete = %v; want ErrForbidden", err)
	}

	for _, eventType := range []repository.OutboxEventType{
		repository.EventTypeFileDeleted11, repository.EventTypeFileNotify11,
	} {
		has, hasErr := repo.HasEventOutboxFact(ctx, obj.ID, eventType)
		if hasErr != nil || has {
			t.Fatalf("denied delete wrote outbox fact %s: has=%v err=%v", eventType, has, hasErr)
		}
	}
	select {
	case event := <-ch:
		t.Fatalf("denied delete broadcast event %+v", event)
	default:
	}
	rows, err := repo.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Action == repository.AuditActionFileDelete {
			t.Fatalf("denied delete wrote audit row %+v", row)
		}
	}
	if _, err := repo.GetObject(ctx, "default", "default", obj.Key); err != nil {
		t.Fatalf("object must survive a denied delete: %v", err)
	}
}

// assertDeleteAuditRow checks that exactly one file.delete row exists with the
// expected actor/detail/tenant and a bucket/key target.
func assertDeleteAuditRow(t *testing.T, repo repository.Repository, actor, detail, tenant string, obj repository.Object) {
	t.Helper()
	rows, err := repo.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var matches []repository.AuditEntry
	for _, row := range rows {
		if row.Action == repository.AuditActionFileDelete {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("file.delete audit rows = %d, want 1: %+v", len(matches), rows)
	}
	got := matches[0]
	if got.Actor != actor || got.Detail != detail || got.TenantID != tenant {
		t.Errorf("audit row = %+v, want actor=%q detail=%q tenant=%q", got, actor, detail, tenant)
	}
	if got.Target != obj.Bucket+"/"+obj.Key {
		t.Errorf("target = %q, want %q", got.Target, obj.Bucket+"/"+obj.Key)
	}
	if got.CreatedAt == "" || got.ID <= 0 {
		t.Errorf("audit row missing created_at/id: %+v", got)
	}
}

// assertDeletedFactVisible checks the deleted@1.1 outbox fact was committed
// for the origin object (payload-level object_id assertions live in the
// repository package, which owns the sqlStore cast).
func assertDeletedFactVisible(t *testing.T, repo repository.Repository, originID int64) {
	t.Helper()
	has, err := repo.HasEventOutboxFact(context.Background(), originID, repository.EventTypeFileDeleted11)
	if err != nil || !has {
		t.Fatalf("deleted@1.1 fact visible: has=%v err=%v", has, err)
	}
}

// TestAdminDelete_EmitsExactlyOneDeletedFact — AC-2 service leg: the admin
// entry shares the single svc.Delete path, so the interface-reachable delete
// semantics (both outbox fact types visible, exactly one audit row, object
// gone) are asserted here. Row-count/status/payload assertions are not
// reachable through the Repository interface (HasEventOutboxFact is bool-only)
// and live in the repository + integration legs (§7.3).
func TestAdminDelete_EmitsExactlyOneDeletedFact(t *testing.T) {
	svc, repo := newTestSvc(t)
	ctx := context.Background()
	obj, err := svc.Put(ctx, "acme", "default", "docs/a.txt", strings.NewReader("body"), 4, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "acme", DefaultBucket, "docs/a.txt", true); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	for _, eventType := range []repository.OutboxEventType{
		repository.EventTypeFileDeleted11, repository.EventTypeFileNotify11,
	} {
		has, err := repo.HasEventOutboxFact(ctx, obj.ID, eventType)
		if err != nil || !has {
			t.Fatalf("outbox fact %s visible: has=%v err=%v", eventType, has, err)
		}
	}
	assertDeleteAuditRow(t, repo, "", "hard", "acme", obj)
	if _, err := repo.GetObject(ctx, "acme", "default", "docs/a.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("GetObject after hard delete = %v, want ErrNotFound", err)
	}
}

// TestAdminDelete_InvalidatesShareAndChunks — AC-3 behavior leg: shares are
// removed in the delete transaction (access.Store cast) and the ChunkCleaner
// hook fires for hard deletes, mirroring the REST path byte-for-byte (both
// share svc.Delete).
func TestAdminDelete_InvalidatesShareAndChunks(t *testing.T) {
	ctx := context.Background()

	t.Run("hard delete removes share row and cleans chunks", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		var cleaned int64
		svc.WithChunkCleaner(&mockChunkCleaner{
			fn: func(_ context.Context, objectID int64) error {
				cleaned = objectID
				return nil
			},
		})
		obj, err := svc.Put(ctx, "acme", "default", "docs/a.txt", strings.NewReader("body"), 4, PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		shares, ok := repo.(access.Store)
		if !ok {
			t.Fatal("repository does not implement access.Store")
		}
		share := access.Share{
			ID: "share-1", TenantID: "acme", Bucket: "default", Key: "docs/a.txt",
			Name: "s", TokenHash: "th-1", AllowPreview: true,
			CreatedBy: "alice", CreatedAt: time.Now().UTC(),
		}
		if err := shares.CreateShare(ctx, share); err != nil {
			t.Fatal(err)
		}

		if err := svc.Delete(ctx, "acme", DefaultBucket, "docs/a.txt", true); err != nil {
			t.Fatalf("hard delete: %v", err)
		}
		rows, err := shares.ListShares(ctx, "acme", "default", "docs/a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("shares rows = %d, want 0 (in-tx invalidation)", len(rows))
		}
		if cleaned != obj.ID {
			t.Fatalf("chunk cleaner called with objectID=%d, want %d", cleaned, obj.ID)
		}
		if _, err := repo.GetObject(ctx, "acme", "default", "docs/a.txt"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("GetObject = %v, want ErrNotFound", err)
		}
		if _, err := svc.Storage().Stat(ctx, obj.StorageKey); err == nil {
			t.Error("storage blob still present after hard delete")
		}
		assertDeleteAuditRow(t, repo, "", "hard", "acme", obj)
	})

	t.Run("soft delete keeps blob and row, share invalidated", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		svc.WithChunkCleaner(&mockChunkCleaner{
			fn: func(_ context.Context, objectID int64) error {
				return nil
			},
		})
		obj, err := svc.Put(ctx, "acme", "default", "docs/b.txt", strings.NewReader("body"), 4, PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		shares, ok := repo.(access.Store)
		if !ok {
			t.Fatal("repository does not implement access.Store")
		}
		if err := shares.CreateShare(ctx, access.Share{
			ID: "share-2", TenantID: "acme", Bucket: "default", Key: "docs/b.txt",
			Name: "s", TokenHash: "th-2", AllowPreview: true,
			CreatedBy: "alice", CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}

		if err := svc.Delete(ctx, "acme", DefaultBucket, "docs/b.txt", false); err != nil {
			t.Fatalf("soft delete: %v", err)
		}
		got, err := repo.GetObjectByID(ctx, obj.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.DeletedAt == nil {
			t.Error("deleted_at not set after soft delete")
		}
		if _, err := svc.Storage().Stat(ctx, obj.StorageKey); err != nil {
			t.Errorf("storage blob must survive soft delete: %v", err)
		}
		rows, err := shares.ListShares(ctx, "acme", "default", "docs/b.txt")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("shares rows = %d, want 0 after soft delete", len(rows))
		}
		// Soft delete also runs the inline chunk-cleanup safety net (the
		// event-bus subscriber may drop the deletion event), so no negative
		// assertion here.
		assertDeleteAuditRow(t, repo, "", "soft", "acme", obj)
	})

	t.Run("actor from access principal lands on the audit row", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		ctx := access.WithPrincipal(context.Background(), access.Principal{SubjectID: "operator-1"})
		obj := putTestObject(t, svc, "audit-actor.txt", "body")
		if err := svc.Delete(ctx, "", "", obj.Key, true); err != nil {
			t.Fatalf("hard delete: %v", err)
		}
		assertDeleteAuditRow(t, repo, "operator-1", "hard", "default", obj)
	})
}
