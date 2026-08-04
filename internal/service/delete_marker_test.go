package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type objectLookupFailureRepository struct {
	repository.Repository
	err error
}

func (r objectLookupFailureRepository) GetObject(
	context.Context, string, string, string,
) (repository.Object, error) {
	return repository.Object{}, r.err
}

func TestDeleteMarkerPreservesVersionsAndUsage(t *testing.T) {
	svc, repo := newTestSvc(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "", "", "marked.txt", strings.NewReader("one"), 3, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := svc.Put(ctx, "", "", "marked.txt", strings.NewReader("two-two"), 7, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var cleaned int64
	svc.WithChunkCleaner(&mockChunkCleaner{fn: func(_ context.Context, objectID int64) error {
		cleaned = objectID
		return nil
	}})
	marker, err := svc.CreateDeleteMarker(ctx, "", "", "marked.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !IsDeleteMarker(marker) {
		t.Fatalf("created row is not a delete marker: %+v", marker)
	}
	if cleaned != v2.ID {
		t.Fatalf("delete marker cleaned chunks for %d, want %d", cleaned, v2.ID)
	}
	if _, _, err := svc.Get(ctx, "", "", "marked.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("current GET error = %v, want ErrNotFound", err)
	}
	rc, _, err := svc.GetVersion(ctx, "", "", "marked.txt", v2.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	quota, err := repo.GetTenantQuota(ctx, DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	if quota.UsedBytes != 10 || quota.UsedObjects != 2 {
		t.Fatalf("delete marker changed usage: %d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}

	sink := &recordingSink{}
	svc.WithEventSink(sink)
	if err := svc.DeleteVersion(ctx, "", "", "marked.txt", marker.VersionID); err != nil {
		t.Fatalf("delete marker: %v", err)
	}
	if len(sink.events) != 2 || sink.events[0].Type != repository.EventDeleted ||
		sink.events[1].Type != repository.EventCreated || *sink.events[1].ObjectID != v2.ID {
		t.Fatalf("marker removal events = %+v", sink.events)
	}
	assertObjectBody(t, svc, "marked.txt", "two-two")
	versions, err := svc.ListVersions(ctx, "", "", "marked.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].VersionID != v2.VersionID || versions[1].VersionID != v1.VersionID {
		t.Fatalf("versions after marker removal = %+v", versions)
	}
}

func TestCreateDeleteMarkerFailsClosedOnCurrentLookupError(t *testing.T) {
	svc, repo := newTestSvc(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	lookupErr := errors.New("object lookup unavailable")
	broken := NewFileService(
		svc.store,
		objectLookupFailureRepository{Repository: repo, err: lookupErr},
		nil,
	)
	if _, err := broken.CreateDeleteMarker(
		ctx, "", "", "must-not-exist",
	); !errors.Is(err, lookupErr) {
		t.Fatalf("delete marker error = %v, want lookup error", err)
	}
	versions, err := repo.ListObjectVersions(
		ctx, DefaultTenant, DefaultBucket, "must-not-exist",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Fatalf("lookup failure inserted %d delete markers", len(versions))
	}
}

func TestDeletingHistoricalDeleteMarkerDoesNotReemitCurrentVersion(t *testing.T) {
	svc, _ := newTestSvc(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(
		ctx, "", "", "history.txt", strings.NewReader("old"), 3, PutOptions{},
	); err != nil {
		t.Fatal(err)
	}
	marker, err := svc.CreateDeleteMarker(ctx, "", "", "history.txt")
	if err != nil {
		t.Fatal(err)
	}
	current, err := svc.Put(
		ctx, "", "", "history.txt", strings.NewReader("current"), 7, PutOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	svc.WithEventSink(sink)
	if err := svc.DeleteVersion(
		ctx, "", "", "history.txt", marker.VersionID,
	); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 || sink.events[0].Type != repository.EventDeleted {
		t.Fatalf("historical marker deletion events = %+v, want one deleted event", sink.events)
	}
	after, err := svc.Stat(ctx, "", "", "history.txt")
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != current.ID {
		t.Fatalf("current object changed from %d to %d", current.ID, after.ID)
	}
}
