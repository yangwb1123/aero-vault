//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
)

func TestPostgresPutACLEntryRejectsIDResourceMismatch(t *testing.T) {
	repo, _ := freshRepo(t)
	store, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	ctx := context.Background()
	original := access.ACLEntry{
		ID: "pg-acl-bound", TenantID: "tenant-a", Bucket: "bucket-a", Key: "docs/",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
		Inherit: true, CreatedBy: "original", CreatedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
	if err := store.PutACLEntry(ctx, original); err != nil {
		t.Fatalf("seed ACL: %v", err)
	}
	mismatch := original
	mismatch.TenantID = "tenant-b"
	mismatch.PrincipalID = "mallory"
	mismatch.Effect = access.EffectDeny
	if err := store.PutACLEntry(ctx, mismatch); !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("mismatched update error=%v, want ErrInvalidArgument", err)
	}
	got, err := store.GetACLEntryByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("reload ACL: %v", err)
	}
	if got != original {
		t.Fatalf("row changed after rejected update: got=%+v want=%+v", got, original)
	}

	updated := original
	updated.PrincipalID = "bob"
	updated.Action = access.ActionWrite
	updated.Effect = access.EffectDeny
	updated.Inherit = false
	updated.CreatedBy = "forged"
	updated.CreatedAt = time.Now().UTC()
	if err := store.PutACLEntry(ctx, updated); err != nil {
		t.Fatalf("same-resource update: %v", err)
	}
	got, err = store.GetACLEntryByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("reload same-resource update: %v", err)
	}
	if got.TenantID != original.TenantID || got.Bucket != original.Bucket || got.Key != original.Key ||
		got.ResourceKind != original.ResourceKind || got.CreatedBy != original.CreatedBy ||
		!got.CreatedAt.Equal(original.CreatedAt) || got.PrincipalID != updated.PrincipalID ||
		got.Action != updated.Action || got.Effect != updated.Effect || got.Inherit != updated.Inherit {
		t.Fatalf("same-resource update changed unexpected fields: got=%+v", got)
	}

	racing := []access.ACLEntry{
		{ID: "pg-acl-race", TenantID: "tenant-a", Bucket: "bucket-a", Key: "a.txt",
			ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
			PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow},
		{ID: "pg-acl-race", TenantID: "tenant-b", Bucket: "bucket-b", Key: "b.txt",
			ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
			PrincipalID: "bob", Action: access.ActionRead, Effect: access.EffectAllow},
	}
	start := make(chan struct{})
	results := make(chan error, len(racing))
	var group sync.WaitGroup
	for _, entry := range racing {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- store.PutACLEntry(ctx, entry)
		}()
	}
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for writeErr := range results {
		switch {
		case writeErr == nil:
			successes++
		case errors.Is(writeErr, access.ErrInvalidArgument):
		default:
			t.Fatalf("concurrent conflict error=%v", writeErr)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successful writes=%d, want 1", successes)
	}
}
