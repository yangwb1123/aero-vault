package service

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
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
