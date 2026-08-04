package service

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newAccessTestService(t *testing.T) (*FileService, *access.Manager) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatal(err)
	}
	accessStore, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	manager, err := access.NewManager(accessStore, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultTenant,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewFileService(store, repo, nil).WithAuthorizer(manager), manager
}

func principalContext(subject string, scopes ...string) context.Context {
	return access.WithPrincipal(context.Background(), access.Principal{
		SubjectID: subject, TenantID: "acme", Kind: access.PrincipalUser, Scopes: scopes,
	})
}

func TestFileServiceEnforcesDepartmentACLOnAllObjectReads(t *testing.T) {
	svc, manager := newAccessTestService(t)
	alice := principalContext("alice", "read", "write")
	admin := principalContext("admin", "admin")
	body := "0123456789"
	obj, err := svc.Put(alice, "acme", "default", "teams/design.jpg", strings.NewReader(body), int64(len(body)), PutOptions{
		ContentType: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ObjectOwner(obj) != "alice" {
		t.Fatalf("owner=%q want alice", ObjectOwner(obj))
	}
	parent, err := manager.CreateDepartment(admin, access.Department{TenantID: "acme", Name: "engineering"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := manager.CreateDepartment(admin, access.Department{
		TenantID: "acme", ParentID: parent.ID, Name: "platform",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PutDepartmentMember(admin, access.DepartmentMember{
		TenantID: "acme", DepartmentID: child.ID, SubjectID: "bob",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PutACL(admin, access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "teams/",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeDepartment,
		PrincipalID: parent.ID, Action: access.ActionRead, Effect: access.EffectAllow, Inherit: true,
	}); err != nil {
		t.Fatal(err)
	}

	bob := principalContext("bob", "read", "write")
	rc, _, err := svc.GetRange(bob, "acme", "default", obj.Key, 2, 4)
	if err != nil {
		t.Fatalf("department member range read: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(got) != "2345" {
		t.Fatalf("range=%q err=%v", got, err)
	}
	if _, err := svc.StatVersionWithOptions(
		bob, "acme", "default", obj.Key, obj.VersionID, ReadOptions{},
	); err != nil {
		t.Fatalf("department member version stat: %v", err)
	}
	if _, err := svc.Put(bob, "acme", "default", obj.Key, strings.NewReader("x"), 1, PutOptions{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read-only department member write error=%v", err)
	}

	carol := principalContext("carol", "read", "write")
	if _, err := svc.Stat(carol, "acme", "default", obj.Key); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider stat error=%v", err)
	}
	page, err := svc.List(carol, "acme", "default", "teams/", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 0 {
		t.Fatalf("unauthorized list leaked objects: %+v", page.Objects)
	}
}

func TestMultipartSessionUsesResourceACL(t *testing.T) {
	svc, manager := newAccessTestService(t)
	alice := principalContext("alice", "read", "write")
	upload, err := svc.InitMultipart(alice, "acme", "default", "private/large.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	admin := principalContext("admin", "admin")
	if _, err := manager.PutACL(admin, access.ACLEntry{
		TenantID: "acme", Bucket: "default", Key: "private/",
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionWrite, Effect: access.EffectAllow, Inherit: true,
	}); err != nil {
		t.Fatal(err)
	}
	bob := principalContext("bob", "read", "write")
	_, err = svc.UploadPartFor(bob, MultipartScope{
		TenantID: "acme", Bucket: "default", Key: upload.Key,
	}, upload.ID, 1, strings.NewReader("part"), 4, ReadOptions{})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("multipart hijack error=%v", err)
	}
	if _, err := svc.UploadPartFor(alice, MultipartScope{
		TenantID: "acme", Bucket: "default", Key: upload.Key,
	}, upload.ID, 1, strings.NewReader("part"), 4, ReadOptions{}); err != nil {
		t.Fatalf("owner upload part: %v", err)
	}
}

func TestAuthorizedListPaginationDoesNotLeakDeniedMarkers(t *testing.T) {
	svc, manager := newAccessTestService(t)
	alice := principalContext("alice", "read", "write")
	bob := principalContext("bob", "read", "write")
	for _, key := range []string{"a-secret.txt", "c-secret.txt"} {
		if _, err := svc.Put(alice, "acme", "default", key, strings.NewReader(key), int64(len(key)), PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"b-visible.txt", "d-visible.txt"} {
		if _, err := svc.Put(bob, "acme", "default", key, strings.NewReader(key), int64(len(key)), PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	admin := principalContext("admin", "admin")
	for _, key := range []string{"a-secret.txt", "c-secret.txt"} {
		if _, err := manager.PutACL(admin, access.ACLEntry{
			TenantID: "acme", Bucket: "default", Key: key,
			ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
			PrincipalID: "bob", Action: access.ActionRead, Effect: access.EffectDeny,
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List(bob, "acme", "default", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != "b-visible.txt" {
		t.Fatalf("first page=%+v", page)
	}
	if !page.HasMore || page.NextMarker != "b-visible.txt" {
		t.Fatalf("pagination leaked or lost marker: %+v", page)
	}
	next, err := svc.List(bob, "acme", "default", "", page.NextMarker, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Objects) != 1 || next.Objects[0].Key != "d-visible.txt" || next.HasMore {
		t.Fatalf("second page=%+v", next)
	}
}
