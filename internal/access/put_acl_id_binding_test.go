package access_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
)

func TestPutACLRejectsExistingIDForDifferentResource(t *testing.T) {
	manager, store, ctx := testManager(t, access.DefaultDeny)
	original := access.ACLEntry{
		ID: "acl-resource-b", TenantID: "tenant-b", Bucket: "private",
		Key: "secret.txt", ResourceKind: access.ResourceObject,
		PrincipalType: access.PrincipalTypeUser, PrincipalID: "bob",
		Action: access.ActionRead, Effect: access.EffectAllow, Inherit: false,
		CreatedBy: "owner-b", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	seedACL(t, store, original)
	callerPrincipal := access.Principal{
		SubjectID: "alice", TenantID: "tenant-a", Kind: access.PrincipalUser,
	}
	caller := access.WithPrincipal(ctx, callerPrincipal)
	resourceA := access.Resource{
		TenantID: "tenant-a", Bucket: "public", Key: "allowed.txt",
		Kind: access.ResourceObject,
	}
	seedACL(t, store, access.ACLEntry{
		ID: "acl-manage-a", TenantID: resourceA.TenantID, Bucket: resourceA.Bucket,
		Key: resourceA.Key, ResourceKind: resourceA.Kind, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: callerPrincipal.SubjectID, Action: access.ActionManageACL,
		Effect: access.EffectAllow, CreatedBy: "owner-a", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	decision, err := manager.Authorize(caller, callerPrincipal, access.ActionManageACL, resourceA)
	if err != nil || !decision.Allowed {
		t.Fatalf("caller should manage resource A: decision=%+v err=%v", decision, err)
	}
	decision, err = manager.Authorize(caller, callerPrincipal, access.ActionManageACL, access.Resource{
		TenantID: original.TenantID, Bucket: original.Bucket, Key: original.Key, Kind: original.ResourceKind,
	})
	if err != nil || decision.Allowed {
		t.Fatalf("caller should not manage resource B: decision=%+v err=%v", decision, err)
	}
	_, err = manager.PutACL(caller, access.ACLEntry{
		ID: original.ID, TenantID: resourceA.TenantID, Bucket: resourceA.Bucket, Key: resourceA.Key,
		ResourceKind: resourceA.Kind, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "mallory", Action: access.ActionWrite, Effect: access.EffectDeny, Inherit: true,
	})
	if !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("PutACL error=%v, want ErrInvalidArgument", err)
	}
	got, err := store.GetACLEntryByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetACLEntryByID: %v", err)
	}
	if got != original {
		t.Fatalf("ACL changed after rejected rebind: got=%+v want=%+v", got, original)
	}
}

func TestPutACLValidationPrecedesIDLookup(t *testing.T) {
	store := &trackingACLStore{}
	manager, err := access.NewManager(store, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultDeny,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.PutACL(context.Background(), access.ACLEntry{
		ID: "acl-invalid", Bucket: "default", Key: "x.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
	})
	if !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("PutACL error=%v, want ErrInvalidArgument", err)
	}
	if store.lookupCalls != 0 || store.putCalls != 0 {
		t.Fatalf("invalid entry calls lookup=%d put=%d; want 0,0", store.lookupCalls, store.putCalls)
	}
}

func TestPutACLNaturalKeyDoesNotUseGlobalIDLookup(t *testing.T) {
	store := &trackingACLStore{}
	manager, err := access.NewManager(store, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultDeny,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := adminContext(context.Background(), "acme")
	_, err = manager.PutACL(ctx, access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "x.txt", ResourceKind: access.ResourceObject,
		PrincipalType: access.PrincipalTypeUser, PrincipalID: "alice",
		Action: access.ActionRead, Effect: access.EffectAllow,
	})
	if err != nil {
		t.Fatalf("PutACL: %v", err)
	}
	if store.lookupCalls != 0 || store.resourceCalls != 1 || store.putCalls != 1 {
		t.Fatalf("ID-empty calls lookup=%d resource=%d put=%d; want 0,1,1",
			store.lookupCalls, store.resourceCalls, store.putCalls)
	}
}

func TestPutACLExistingIDChecksBindingBeforeAuthorization(t *testing.T) {
	store := &trackingACLStore{persisted: access.ACLEntry{
		ID: "acl-bound", TenantID: "tenant-b", Bucket: "bucket-b", Key: "b.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "bob", Action: access.ActionRead, Effect: access.EffectAllow,
	}}
	manager, err := access.NewManager(store, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultDeny,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithPrincipal(context.Background(), access.Principal{
		SubjectID: "alice", TenantID: "tenant-a", Kind: access.PrincipalUser,
	})
	_, err = manager.PutACL(ctx, access.ACLEntry{
		ID: "acl-bound", TenantID: "tenant-a", Bucket: "bucket-a", Key: "a.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "mallory", Action: access.ActionWrite, Effect: access.EffectAllow,
	})
	if !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("PutACL error=%v, want ErrInvalidArgument", err)
	}
	if store.lookupCalls != 1 || store.applicableCalls != 0 || store.departmentCalls != 0 || store.putCalls != 0 {
		t.Fatalf("calls lookup=%d applicable=%d departments=%d put=%d; want 1,0,0,0",
			store.lookupCalls, store.applicableCalls, store.departmentCalls, store.putCalls)
	}
}

func TestPutACLWithExistingIDSameResourcePreservesCreatedMetadata(t *testing.T) {
	manager, store, ctx := testManager(t, access.DefaultDeny)
	createdAt := time.Date(2025, 11, 12, 13, 14, 15, 16, time.UTC)
	original := access.ACLEntry{
		ID: "acl-legacy-folder", TenantID: "acme", Bucket: "default", Key: "docs",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
		Inherit: true, CreatedBy: "original-creator", CreatedAt: createdAt,
	}
	seedACL(t, store, original)
	updated, err := manager.PutACL(adminContext(ctx, "acme"), access.ACLEntry{
		ID: original.ID, TenantID: "acme", Bucket: "default", Key: "/docs/",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "bob", Action: access.ActionWrite, Effect: access.EffectDeny,
		Inherit: false, CreatedBy: "forged", CreatedAt: createdAt.Add(time.Hour), OwnerID: "owner",
	})
	if err != nil {
		t.Fatalf("PutACL: %v", err)
	}
	if updated.ID != original.ID || updated.Key != original.Key || updated.CreatedBy != original.CreatedBy ||
		!updated.CreatedAt.Equal(original.CreatedAt) || updated.OwnerID != "owner" {
		t.Fatalf("returned ACL identity/metadata = %+v", updated)
	}
	got, err := store.GetACLEntryByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetACLEntryByID: %v", err)
	}
	if got.ID != original.ID || got.TenantID != original.TenantID || got.Bucket != original.Bucket ||
		got.Key != original.Key || got.ResourceKind != original.ResourceKind ||
		got.CreatedBy != original.CreatedBy || !got.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("persisted identity/metadata changed: got=%+v original=%+v", got, original)
	}
	if got.PrincipalID != "bob" || got.Action != access.ActionWrite || got.Effect != access.EffectDeny || got.Inherit {
		t.Fatalf("mutable ACL fields were not updated: %+v", got)
	}
}

func TestPutACLExistingIDRequiresManageAuthorization(t *testing.T) {
	manager, store, ctx := testManager(t, access.DefaultDeny)
	original := access.ACLEntry{
		ID: "acl-unauthorized", TenantID: "acme", Bucket: "default", Key: "docs",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "bob", Action: access.ActionRead, Effect: access.EffectAllow,
		CreatedBy: "owner", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	seedACL(t, store, original)
	caller := access.WithPrincipal(ctx, access.Principal{
		SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser,
	})
	_, err := manager.PutACL(caller, access.ACLEntry{
		ID: original.ID, TenantID: original.TenantID, Bucket: original.Bucket, Key: "/docs",
		ResourceKind: original.ResourceKind, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "mallory", Action: access.ActionWrite, Effect: access.EffectDeny,
	})
	if !errors.Is(err, access.ErrDenied) {
		t.Fatalf("PutACL error=%v, want ErrDenied", err)
	}
	got, err := store.GetACLEntryByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetACLEntryByID: %v", err)
	}
	if got != original {
		t.Fatalf("ACL changed after unauthorized update: got=%+v want=%+v", got, original)
	}
}

func TestPutACLWithExistingIDNormalizesObjectAndBucketIdentity(t *testing.T) {
	manager, store, ctx := testManager(t, access.DefaultDeny)
	cases := []struct {
		name, id, key, requestKey string
		kind                      access.ResourceKind
	}{
		{name: "object leading slash", id: "acl-object-normalized", key: "object.txt", requestKey: "/object.txt", kind: access.ResourceObject},
		{name: "bucket ignores key", id: "acl-bucket-normalized", key: "", requestKey: "ignored", kind: access.ResourceBucket},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := access.ACLEntry{
				ID: tc.id, TenantID: "acme", Bucket: "default", Key: tc.key,
				ResourceKind: tc.kind, PrincipalType: access.PrincipalTypeUser,
				PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
				CreatedBy: "seed", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			}
			seedACL(t, store, original)
			updated, err := manager.PutACL(adminContext(ctx, "acme"), access.ACLEntry{
				ID: tc.id, TenantID: "acme", Bucket: "default", Key: tc.requestKey,
				ResourceKind: tc.kind, PrincipalType: access.PrincipalTypeUser,
				PrincipalID: "bob", Action: access.ActionWrite, Effect: access.EffectDeny,
			})
			if err != nil {
				t.Fatalf("PutACL: %v", err)
			}
			if updated.Key != original.Key || updated.ResourceKind != original.ResourceKind {
				t.Fatalf("returned resource identity=%+v, want key=%q kind=%q", updated, original.Key, original.ResourceKind)
			}
			got, err := store.GetACLEntryByID(ctx, tc.id)
			if err != nil {
				t.Fatalf("GetACLEntryByID: %v", err)
			}
			if got.Key != original.Key || got.PrincipalID != "bob" || got.Effect != access.EffectDeny {
				t.Fatalf("stored ACL=%+v, want preserved key and mutable update", got)
			}
		})
	}
}

func TestPutACLWithExistingIDPreservesEmptyCreationMetadata(t *testing.T) {
	manager, store, ctx := testManager(t, access.DefaultDeny)
	original := access.ACLEntry{
		ID: "acl-empty-created-at", TenantID: "acme", Bucket: "default", Key: "empty",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
		CreatedBy: "legacy-creator",
	}
	seedACL(t, store, original)
	updated, err := manager.PutACL(adminContext(ctx, "acme"), access.ACLEntry{
		ID: original.ID, TenantID: original.TenantID, Bucket: original.Bucket, Key: "/empty",
		ResourceKind: original.ResourceKind, PrincipalType: original.PrincipalType,
		PrincipalID: "bob", Action: access.ActionWrite, Effect: access.EffectDeny,
		CreatedBy: "forged", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("PutACL: %v", err)
	}
	if updated.CreatedBy != original.CreatedBy || !updated.CreatedAt.IsZero() {
		t.Fatalf("empty persisted creation metadata was replaced: %+v", updated)
	}
	got, err := store.GetACLEntryByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetACLEntryByID: %v", err)
	}
	if got.CreatedBy != original.CreatedBy || !got.CreatedAt.IsZero() {
		t.Fatalf("stored creation metadata changed: %+v", got)
	}
}

func TestPutACLWithUnusedIDCreatesEntry(t *testing.T) {
	manager, store, ctx := testManager(t, access.DefaultDeny)
	created, err := manager.PutACL(adminContext(ctx, "acme"), access.ACLEntry{
		ID: "acl-supplied", TenantID: "acme", Bucket: "default", Key: "new.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
	})
	if err != nil {
		t.Fatalf("PutACL: %v", err)
	}
	if created.ID != "acl-supplied" || created.CreatedBy != "admin" || created.CreatedAt.IsZero() {
		t.Fatalf("created ACL=%+v", created)
	}
	stored, err := store.GetACLEntryByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetACLEntryByID: %v", err)
	}
	if stored.ID != created.ID || stored.TenantID != created.TenantID || stored.Key != created.Key {
		t.Fatalf("stored ACL=%+v", stored)
	}
}

func TestPutACLByIDLookupFailureDoesNotWrite(t *testing.T) {
	probeErr := errors.New("ACL lookup unavailable")
	store := &trackingACLStore{lookupErr: probeErr}
	manager, err := access.NewManager(store, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultDeny,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.PutACL(context.Background(), access.ACLEntry{
		ID: "acl-lookup", TenantID: "acme", Bucket: "default", Key: "x.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: access.EffectAllow,
	})
	if !errors.Is(err, probeErr) {
		t.Fatalf("PutACL error=%v, want lookup error %v", err, probeErr)
	}
	if store.putCalls != 0 {
		t.Fatalf("PutACLEntry calls=%d, want 0", store.putCalls)
	}
}

func TestPutACLIDErrorContract(t *testing.T) {
	manager, store, ctx := testManager(t, access.DefaultDeny)
	seedACL(t, store, access.ACLEntry{
		ID: "acl-existing", TenantID: "acme", Bucket: "default", Key: "other.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "bob", Action: access.ActionRead, Effect: access.EffectAllow,
	})
	caller := access.WithPrincipal(ctx, access.Principal{
		SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser,
	})
	mismatch := access.ACLEntry{
		ID: "acl-existing", TenantID: "acme", Bucket: "default", Key: "target.txt",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "mallory", Action: access.ActionWrite, Effect: access.EffectAllow,
	}
	if _, err := manager.PutACL(caller, mismatch); !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("existing mismatched ID error=%v, want ErrInvalidArgument", err)
	}
	unknown := mismatch
	unknown.ID = "acl-unknown"
	if _, err := manager.PutACL(caller, unknown); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("unknown ID for unauthorized caller error=%v, want ErrDenied", err)
	}
}

func seedACL(t *testing.T, store access.Store, entry access.ACLEntry) {
	t.Helper()
	if err := store.PutACLEntry(context.Background(), entry); err != nil {
		t.Fatalf("seed ACL %q: %v", entry.ID, err)
	}
}

type trackingACLStore struct {
	access.Store
	persisted       access.ACLEntry
	lookupErr       error
	lookupCalls     int
	applicableCalls int
	departmentCalls int
	putCalls        int
	resourceCalls   int
}

func (s *trackingACLStore) GetACLEntryByID(context.Context, string) (access.ACLEntry, error) {
	s.lookupCalls++
	if s.lookupErr != nil {
		return access.ACLEntry{}, s.lookupErr
	}
	return s.persisted, nil
}

func (s *trackingACLStore) ListApplicableACL(context.Context, string, string, string) ([]access.ACLEntry, error) {
	s.applicableCalls++
	return nil, nil
}

func (s *trackingACLStore) ListResourceACL(context.Context, string, string, string, access.ResourceKind) ([]access.ACLEntry, error) {
	s.resourceCalls++
	return nil, nil
}

func (s *trackingACLStore) ListSubjectDepartments(context.Context, string, string) ([]string, error) {
	s.departmentCalls++
	return nil, nil
}

func (s *trackingACLStore) PutACLEntry(context.Context, access.ACLEntry) error {
	s.putCalls++
	return nil
}
