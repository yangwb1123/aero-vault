package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type errorDeleteAuthorizer struct{ err error }

func (a errorDeleteAuthorizer) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{}, a.err
}

func TestDeleteAuthorizationFailClosedForVersionPaths(t *testing.T) {
	t.Run("version delete", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		ctx := context.Background()
		if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
			t.Fatal(err)
		}
		v1 := putTestObject(t, svc, "denied-version.txt", "one")
		v2 := putTestObject(t, svc, "denied-version.txt", "two")
		svc.WithAuthorizer(denyAuthorizer{reason: "default_deny"})

		if err := svc.DeleteVersion(ctx, "", "", v2.Key, v2.VersionID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("DeleteVersion = %v; want ErrForbidden", err)
		}
		versions, err := repo.ListObjectVersions(ctx, DefaultTenant, DefaultBucket, v2.Key)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 2 || versions[0].ID != v2.ID || versions[1].ID != v1.ID {
			t.Fatalf("versions after denied delete = %+v; want both originals", versions)
		}
		if _, err := svc.store.Stat(ctx, v2.StorageKey); err != nil {
			t.Fatalf("denied version delete removed storage: %v", err)
		}
		assertNoDeleteFacts(t, repo, v2.ID)
	})

	t.Run("delete marker with current object", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		ctx := context.Background()
		if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
			t.Fatal(err)
		}
		obj := putTestObject(t, svc, "denied-marker.txt", "body")
		svc.WithAuthorizer(denyAuthorizer{reason: "default_deny"})

		if _, err := svc.CreateDeleteMarker(ctx, "", "", obj.Key); !errors.Is(err, ErrForbidden) {
			t.Fatalf("CreateDeleteMarker = %v; want ErrForbidden", err)
		}
		versions, err := repo.ListObjectVersions(ctx, DefaultTenant, DefaultBucket, obj.Key)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 1 || IsDeleteMarker(versions[0]) {
			t.Fatalf("versions after denied marker = %+v; want original only", versions)
		}
		assertNoDeleteFacts(t, repo, obj.ID)
	})

	t.Run("delete marker without current object", func(t *testing.T) {
		svc, repo := newTestSvc(t)
		ctx := context.Background()
		if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
			t.Fatal(err)
		}
		svc.WithAuthorizer(denyAuthorizer{reason: "default_deny"})

		if _, err := svc.CreateDeleteMarker(ctx, "", "", "missing-marker.txt"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("CreateDeleteMarker missing current = %v; want ErrForbidden", err)
		}
		versions, err := repo.ListObjectVersions(ctx, DefaultTenant, DefaultBucket, "missing-marker.txt")
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 0 {
			t.Fatalf("denied marker created %d rows", len(versions))
		}
	})
}

func TestDeleteAuthorizationFailClosedWithoutProvider(t *testing.T) {
	withProvider, repo := newTestSvc(t)
	obj := putTestObject(t, withProvider, "bare-delete.txt", "body")
	bare := NewFileService(withProvider.store, repo, nil)

	if err := bare.Delete(context.Background(), "", "", obj.Key, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bare Delete = %v; want ErrForbidden", err)
	}
	if _, err := repo.GetObject(context.Background(), DefaultTenant, DefaultBucket, obj.Key); err != nil {
		t.Fatalf("object changed after bare delete denial: %v", err)
	}
	assertNoDeleteFacts(t, repo, obj.ID)
}

func TestDeleteAuthorizationProviderErrorFailsClosed(t *testing.T) {
	svc, repo := newTestSvc(t)
	obj := putTestObject(t, svc, "provider-error.txt", "body")
	providerErr := errors.New("authorization provider unavailable")
	svc.WithAuthorizer(errorDeleteAuthorizer{err: providerErr})

	err := svc.Delete(context.Background(), "", "", obj.Key, true)
	if err == nil || errors.Is(err, ErrForbidden) || !errors.Is(err, providerErr) {
		t.Fatalf("Delete = %v; want provider error, not ErrForbidden", err)
	}
	if _, err := repo.GetObject(context.Background(), DefaultTenant, DefaultBucket, obj.Key); err != nil {
		t.Fatalf("object changed after provider error: %v", err)
	}
	assertNoDeleteFacts(t, repo, obj.ID)
}

func TestAntivirusSystemDeleteExemptionWithoutProvider(t *testing.T) {
	withProvider, repo := newTestSvc(t)
	obj := putTestObject(t, withProvider, "quarantine.txt", "infected")
	bare := NewFileService(withProvider.store, repo, nil)

	ctx := access.AntivirusContext(context.Background(), obj.TenantID)
	if err := bare.QuarantineObjectByID(ctx, obj.ID, "EICAR"); err != nil {
		t.Fatalf("antivirus quarantine with nil provider = %v; want success", err)
	}
	got, err := repo.GetObjectByID(context.Background(), obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt == nil {
		t.Fatal("quarantined object was not soft-deleted")
	}
}

func TestSystemContextDeleteExemptionWithoutProvider(t *testing.T) {
	withProvider, repo := newTestSvc(t)
	obj := putTestObject(t, withProvider, "system-delete.txt", "internal")
	bare := NewFileService(withProvider.store, repo, nil)
	ctx := access.SystemContext(context.Background(), obj.TenantID)
	if err := bare.Delete(ctx, "", "", obj.Key, true); err != nil {
		t.Fatalf("system delete with nil provider = %v; want success", err)
	}
	if _, err := repo.GetObject(context.Background(), obj.TenantID, obj.Bucket, obj.Key); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("system delete object lookup error=%v; want not found", err)
	}
}

func assertNoDeleteFacts(t *testing.T, repo repository.Repository, objectID int64) {
	t.Helper()
	for _, eventType := range []repository.OutboxEventType{
		repository.EventTypeFileDeleted11,
		repository.EventTypeFileNotify11,
	} {
		has, err := repo.HasEventOutboxFact(context.Background(), objectID, eventType)
		if err != nil {
			t.Fatal(err)
		}
		if has {
			t.Fatalf("denied delete wrote %s for object %d", eventType, objectID)
		}
	}
	rows, err := repo.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Action == repository.AuditActionFileDelete {
			t.Fatalf("denied delete wrote audit row %+v", row)
		}
	}
}
