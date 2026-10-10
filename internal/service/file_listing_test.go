package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func putOwnedListingObject(t *testing.T, svc *FileService, owner, key string) repository.Object {
	t.Helper()
	obj, err := svc.Put(principalContext(owner, "read", "write"), "acme", "default", key,
		strings.NewReader("data"), 4, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func assertListingKeys(t *testing.T, page repository.ListPage, want ...string) {
	t.Helper()
	if len(page.Objects) != len(want) {
		t.Fatalf("objects=%+v, want keys %v", page.Objects, want)
	}
	for i, key := range want {
		if page.Objects[i].Key != key {
			t.Fatalf("key[%d]=%q, want %q", i, page.Objects[i].Key, key)
		}
	}
}

func TestMineListingFiltersBeforePaginationAndKeepsACL(t *testing.T) {
	svc, manager := newAccessTestService(t)
	for _, key := range []string{"docs/a-other.txt", "docs/c-other.txt", "docs/z-other.txt"} {
		putOwnedListingObject(t, svc, "bob", key)
	}
	for _, key := range []string{"docs/b-mine.txt", "docs/d-mine.txt", "docs/e-denied.txt"} {
		putOwnedListingObject(t, svc, "alice", key)
	}
	putOwnedListingObject(t, svc, "alice", "outside.txt")
	_, err := manager.PutACL(principalContext("admin", "admin"), access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "docs/e-denied.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectDeny,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := principalContext("alice", "read")
	page, err := svc.ListWithOptions(ctx, "acme", "default", "docs/", "", 1,
		FileListOptions{View: FileListMine})
	if err != nil {
		t.Fatal(err)
	}
	assertListingKeys(t, page, "docs/b-mine.txt")
	if !page.HasMore || page.NextMarker != "docs/b-mine.txt" {
		t.Fatalf("pagination exposes a foreign/denied marker or loses continuation: %+v", page)
	}
	next, err := svc.ListWithOptions(ctx, "acme", "default", "docs/", page.NextMarker, 1,
		FileListOptions{View: FileListMine})
	if err != nil {
		t.Fatal(err)
	}
	assertListingKeys(t, next, "docs/d-mine.txt")
	if next.HasMore || next.NextMarker != "" {
		t.Fatalf("last owned page=%+v", next)
	}
	all, err := svc.ListWithOptions(ctx, "acme", "default", "docs/", "", 20, FileListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertListingKeys(t, all, "docs/a-other.txt", "docs/b-mine.txt", "docs/c-other.txt", "docs/d-mine.txt", "docs/z-other.txt")
}

func TestMineListingDeletedAndTrustedOwnership(t *testing.T) {
	svc, repo := newTestSvc(t)
	mine := putOwnedListingObject(t, svc, "alice", "mine.txt")
	other := putOwnedListingObject(t, svc, "bob", "other.txt")
	legacy, err := svc.Put(context.Background(), "acme", "default", "legacy.txt", strings.NewReader(""), 0, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []repository.Object{mine, other, legacy} {
		if err := repo.SoftDeleteObject(context.Background(), "acme", "default", obj.Key); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.ListWithOptions(principalContext("alice", "read"), "acme", "default", "", "", 10,
		FileListOptions{View: FileListMine, Deleted: true})
	if err != nil {
		t.Fatal(err)
	}
	assertListingKeys(t, page, "mine.txt")
	page, err = svc.ListWithOptions(principalContext("alice", "read"), "acme", "default", "", "", 10,
		FileListOptions{View: FileListMine})
	if err != nil {
		t.Fatal(err)
	}
	assertListingKeys(t, page)
}

func TestMineListingIdentityCannotBeForgedByScopeOrCapability(t *testing.T) {
	svc, _ := newTestSvc(t)
	svc.WithAuthorizer(nil)
	for _, principal := range []access.Principal{
		{},
		{SubjectID: "alice", TenantID: "other", Kind: access.PrincipalUser},
		{SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalAnonymous},
		{SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalShare},
		{SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalPublic},
		{SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalSystem},
		{TenantID: "acme", Kind: access.PrincipalUser},
	} {
		ctx := access.WithPrincipal(context.Background(), principal)
		_, err := svc.ListWithOptions(ctx, "acme", "default", "", "", 10, FileListOptions{View: FileListMine})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("principal=%+v, err=%v, want forbidden", principal, err)
		}
	}
	_, err := svc.ListWithOptions(context.Background(), "acme", "default", "", "", 10,
		FileListOptions{View: "alice"})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("unknown view err=%v", err)
	}
}

func TestMineListingTenantAndBucketIsolationWithoutAuthorizer(t *testing.T) {
	svc, _ := newTestSvc(t)
	putOwnedListingObject(t, svc, "alice", "mine.txt")
	putOwnedListingObject(t, svc, "bob", "other.txt")
	for _, location := range [][2]string{{"other", "default"}, {"acme", "other-bucket"}} {
		ctx := access.WithPrincipal(context.Background(), access.Principal{
			SubjectID: "alice", TenantID: location[0], Kind: access.PrincipalUser,
		})
		if _, err := svc.Put(ctx, location[0], location[1], "foreign.txt", strings.NewReader(""), 0, PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	svc.WithAuthorizer(nil)
	page, err := svc.ListWithOptions(principalContext("alice"), "acme", "default", "", "", 1,
		FileListOptions{View: FileListMine})
	if err != nil {
		t.Fatal(err)
	}
	assertListingKeys(t, page, "mine.txt")
	if page.HasMore || page.NextMarker != "" {
		t.Fatalf("foreign objects affected pagination: %+v", page)
	}
}

func TestMineListingSupportsServiceOwnersAndTenantSuspension(t *testing.T) {
	svc, repo := newTestSvc(t)
	ctx := access.WithPrincipal(context.Background(), access.Principal{
		SubjectID: "app-1", TenantID: "acme", Kind: access.PrincipalService,
	})
	if _, err := svc.Put(ctx, "acme", "default", "service.txt", strings.NewReader(""), 0, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	svc.WithTenantStatusEnforcement()
	page, err := svc.ListWithOptions(ctx, "acme", "default", "", "", 10, FileListOptions{View: FileListMine})
	if err != nil {
		t.Fatal(err)
	}
	assertListingKeys(t, page, "service.txt")
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{
		TenantID: "acme", Status: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ListWithOptions(ctx, "acme", "default", "", "", 10, FileListOptions{View: FileListMine})
	if !errors.Is(err, ErrTenantDisabled) {
		t.Fatalf("suspended tenant listing err=%v", err)
	}
}

// Sparse raw pages must be consumed until one extra owned, readable match is
// found; an empty filtered page is not the end of the owned listing.
type sparseOwnedListingRepo struct {
	repository.Repository
	calls int
}

func (r *sparseOwnedListingRepo) ListObjects(
	_ context.Context, tenant, bucket, _ string, marker string, _ int,
) (repository.ListPage, error) {
	r.calls++
	key, owner, more := "a-other", "bob", true
	switch marker {
	case "a-other":
		key, owner = "b-mine", "alice"
	case "b-mine":
		key, owner = "c-other", "bob"
	case "c-other":
		key, owner, more = "d-mine", "alice", false
	}
	obj := repository.Object{TenantID: tenant, Bucket: bucket, Key: key,
		Metadata: map[string]string{ownerMetadataKey: owner}}
	return repository.ListPage{Objects: []repository.Object{obj}, HasMore: more, NextMarker: key}, nil
}

func TestMineListingScansSparseRawPagesWithoutAuthorizer(t *testing.T) {
	svc, base := newTestSvc(t)
	repo := &sparseOwnedListingRepo{Repository: base}
	svc.repo = repo
	svc.WithAuthorizer(nil)
	page, err := svc.ListWithOptions(principalContext("alice"), "acme", "default", "", "", 1,
		FileListOptions{View: FileListMine})
	if err != nil {
		t.Fatal(err)
	}
	assertListingKeys(t, page, "b-mine")
	if !page.HasMore || page.NextMarker != "b-mine" || repo.calls != 4 {
		t.Fatalf("page=%+v, raw fetches=%d", page, repo.calls)
	}
}
