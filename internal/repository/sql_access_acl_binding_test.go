package repository

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
)

func TestGetACLEntryByID(t *testing.T) {
	store, ctx := openACLTestRepo(t)
	want := access.ACLEntry{
		ID: "acl-global", TenantID: "tenant-a", Bucket: "bucket-a", Key: "file.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
		CreatedBy: "seed", CreatedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
	if err := store.PutACLEntry(ctx, want); err != nil {
		t.Fatalf("PutACLEntry: %v", err)
	}
	got, err := store.GetACLEntryByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetACLEntryByID: %v", err)
	}
	if got != want {
		t.Fatalf("got=%+v, want=%+v", got, want)
	}
	if _, err := store.GetACLEntryByID(ctx, "missing"); !errors.Is(err, access.ErrNotFound) {
		t.Fatalf("missing lookup error=%v, want ErrNotFound", err)
	}
}

func TestPutACLEntryRejectsIDResourceMismatch(t *testing.T) {
	store, ctx := openACLTestRepo(t)
	original := access.ACLEntry{
		ID: "acl-bound", TenantID: "tenant-a", Bucket: "bucket-a", Key: "docs/",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
		Inherit: true, CreatedBy: "original", CreatedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
	if err := store.PutACLEntry(ctx, original); err != nil {
		t.Fatalf("seed ACL: %v", err)
	}
	mismatches := []access.ACLEntry{
		{TenantID: "tenant-b", Bucket: original.Bucket, Key: original.Key, ResourceKind: original.ResourceKind},
		{TenantID: original.TenantID, Bucket: "bucket-b", Key: original.Key, ResourceKind: original.ResourceKind},
		{TenantID: original.TenantID, Bucket: original.Bucket, Key: "other/", ResourceKind: original.ResourceKind},
		{TenantID: original.TenantID, Bucket: original.Bucket, Key: original.Key, ResourceKind: access.ResourceObject},
	}
	for i, mismatch := range mismatches {
		mismatch.ID = original.ID
		mismatch.PrincipalType = access.PrincipalTypeUser
		mismatch.PrincipalID = "mallory"
		mismatch.Action = access.ActionWrite
		mismatch.Effect = access.EffectDeny
		mismatch.CreatedBy = "forged"
		mismatch.CreatedAt = time.Now().UTC()
		if err := store.PutACLEntry(ctx, mismatch); !errors.Is(err, access.ErrInvalidArgument) {
			t.Fatalf("mismatch %d error=%v, want ErrInvalidArgument", i, err)
		}
		got, err := store.GetACLEntryByID(ctx, original.ID)
		if err != nil {
			t.Fatalf("reload after mismatch %d: %v", i, err)
		}
		if got != original {
			t.Fatalf("row changed after mismatch %d: got=%+v want=%+v", i, got, original)
		}
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
	got, err := store.GetACLEntryByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("reload after update: %v", err)
	}
	if got.TenantID != original.TenantID || got.Bucket != original.Bucket || got.Key != original.Key ||
		got.ResourceKind != original.ResourceKind || got.CreatedBy != original.CreatedBy ||
		!got.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("identity/creation metadata changed: got=%+v original=%+v", got, original)
	}
	if got.PrincipalID != updated.PrincipalID || got.Action != updated.Action ||
		got.Effect != updated.Effect || got.Inherit != updated.Inherit {
		t.Fatalf("mutable fields not updated: got=%+v", got)
	}
	if err := store.PutACLEntry(ctx, got); err != nil {
		t.Fatalf("same-resource no-op update: %v", err)
	}
}

func TestPutACLEntryPreservesRawLegacyFolderKey(t *testing.T) {
	store, ctx := openACLTestRepo(t)
	legacy := access.ACLEntry{
		ID: "acl-legacy", TenantID: "acme", Bucket: "default", Key: "docs",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
		CreatedBy: "seed", CreatedAt: time.Now().UTC(),
	}
	if err := store.PutACLEntry(ctx, legacy); err != nil {
		t.Fatalf("seed legacy ACL: %v", err)
	}
	canonical := legacy
	canonical.Key = "docs/"
	canonical.Effect = access.EffectDeny
	if err := store.PutACLEntry(ctx, canonical); !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("canonical raw-key update error=%v, want ErrInvalidArgument", err)
	}
	exact := legacy
	exact.Effect = access.EffectDeny
	if err := store.PutACLEntry(ctx, exact); err != nil {
		t.Fatalf("exact legacy update: %v", err)
	}
	got, err := store.GetACLEntryByID(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("reload legacy ACL: %v", err)
	}
	if got.Key != "docs" || got.Effect != access.EffectDeny {
		t.Fatalf("legacy ACL=%+v, want raw key and updated effect", got)
	}
}

func TestPutACLEntryChecksRowsAffectedErrors(t *testing.T) {
	rowsErr := errors.New("rows affected unavailable")
	if err := validateACLWriteResult(aclResultStub{err: rowsErr}); !errors.Is(err, rowsErr) {
		t.Fatalf("RowsAffected error=%v, want %v", err, rowsErr)
	}
	if err := validateACLWriteResult(aclResultStub{}); !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("zero affected error=%v, want ErrInvalidArgument", err)
	}
	if err := validateACLWriteResult(aclResultStub{affected: 1}); err != nil {
		t.Fatalf("one affected error=%v, want nil", err)
	}
}

type aclResultStub struct {
	affected int64
	err      error
}

func (r aclResultStub) RowsAffected() (int64, error) { return r.affected, r.err }

func TestACLEntryDatabaseErrorsAreReturned(t *testing.T) {
	ctx := context.Background()
	repo, err := Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "acl.db"))
	if err != nil {
		t.Fatal(err)
	}
	store, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetACLEntryByID(ctx, "closed"); err == nil || errors.Is(err, access.ErrNotFound) {
		t.Fatalf("closed database lookup error=%v, want database error", err)
	}
	if err := store.PutACLEntry(ctx, access.ACLEntry{ID: "closed", TenantID: "acme", Bucket: "default"}); err == nil {
		t.Fatal("closed database write unexpectedly succeeded")
	}
}

func TestPutACLEntryConcurrentIDConflict(t *testing.T) {
	store, ctx := openACLTestRepo(t)
	entries := []access.ACLEntry{
		{ID: "acl-race", TenantID: "tenant-a", Bucket: "bucket-a", Key: "a.txt",
			ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
			PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
			CreatedBy: "writer-a", CreatedAt: time.Now().UTC()},
		{ID: "acl-race", TenantID: "tenant-b", Bucket: "bucket-b", Key: "b.txt",
			ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
			PrincipalID: "bob", Action: access.ActionRead, Effect: access.EffectAllow,
			CreatedBy: "writer-b", CreatedAt: time.Now().UTC()},
	}
	start := make(chan struct{})
	results := make(chan error, len(entries))
	var group sync.WaitGroup
	for _, entry := range entries {
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
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, access.ErrInvalidArgument):
		default:
			t.Fatalf("concurrent conflict error=%v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful writes=%d, want 1", successes)
	}
	got, err := store.GetACLEntryByID(ctx, "acl-race")
	if err != nil {
		t.Fatalf("get winning row: %v", err)
	}
	switch {
	case got.TenantID == "tenant-a" && got.Bucket == "bucket-a" && got.Key == "a.txt":
	case got.TenantID == "tenant-b" && got.Bucket == "bucket-b" && got.Key == "b.txt":
	default:
		t.Fatalf("winning row has mixed or unexpected identity: %+v", got)
	}
}
