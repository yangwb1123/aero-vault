package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestPutOverwriteAccountsOnlyUsageDelta(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if _, err := svc.Put(ctx, "", "", "same.txt", strings.NewReader("0123456789"), 10, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetTenantQuota(ctx, "default", 10, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "", "", "same.txt", strings.NewReader("new"), 3, PutOptions{}); err != nil {
		t.Fatalf("overwrite at object cap must succeed: %v", err)
	}
	assertTenantUsage(t, repo, 3, 1)
}

func TestPutZeroByteObjectCountsTowardObjectQuota(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if _, err := svc.Put(ctx, "", "", "empty", strings.NewReader(""), 0, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	assertTenantUsage(t, repo, 0, 1)
	if err := repo.SetTenantQuota(ctx, "default", 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "", "", "another", strings.NewReader(""), 0, PutOptions{}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("second empty object should exceed object quota, got %v", err)
	}
}

func TestCompleteMultipartOverwriteAccountsOnlyUsageDelta(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if _, err := svc.Put(ctx, "", "", "same.bin", strings.NewReader("0123456789"), 10, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetTenantQuota(ctx, "default", 10, 1); err != nil {
		t.Fatal(err)
	}
	upload, err := svc.InitMultipart(ctx, "", "", "same.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(ctx, upload.ID, 1, strings.NewReader("new"), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMultipart(ctx, upload.ID); err != nil {
		t.Fatalf("multipart overwrite at object cap must succeed: %v", err)
	}
	assertTenantUsage(t, repo, 3, 1)
}

func TestMultipartRespectsBucketQuota(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	if err := svc.SetBucketQuota(ctx, "", "", 4, 1); err != nil {
		t.Fatal(err)
	}
	upload, err := svc.InitMultipart(ctx, "", "", "large.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(ctx, upload.ID, 1, strings.NewReader("12345"), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMultipart(ctx, upload.ID); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("multipart above bucket cap should fail, got %v", err)
	}
}

func TestRestoreAccountsQuotaAndRestoresOnlyDeletedCurrent(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	sink := &recordingSink{}
	svc.WithEventSink(sink)
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "", "", "restore.txt", strings.NewReader("old"), 3, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	current, err := svc.Put(ctx, "", "", "restore.txt", strings.NewReader("current"), 7, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "", "", "restore.txt", false); err != nil {
		t.Fatal(err)
	}
	assertTenantUsage(t, repo, 3, 1)
	if err := svc.RestoreObject(ctx, "", "", "restore.txt"); err != nil {
		t.Fatal(err)
	}
	assertTenantUsage(t, repo, 10, 2)
	restored, err := svc.Stat(ctx, "", "", "restore.txt")
	if err != nil {
		t.Fatal(err)
	}
	if restored.VersionID != current.VersionID {
		t.Fatalf("restored version = %q, want %q", restored.VersionID, current.VersionID)
	}
	if sink.events[len(sink.events)-1].Type != repository.EventCreated {
		t.Fatalf("restore event = %q, want created", sink.events[len(sink.events)-1].Type)
	}
}

func TestHardDeleteRemovesEveryVersionAndUsage(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"one", "second"} {
		if _, err := svc.Put(ctx, "", "", "history.txt", strings.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := svc.ListVersions(ctx, "", "", "history.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "", "", "history.txt", true); err != nil {
		t.Fatal(err)
	}
	assertTenantUsage(t, repo, 0, 0)
	for _, version := range versions {
		if _, err := svc.Storage().Stat(ctx, version.StorageKey); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("version blob %q remains: %v", version.StorageKey, err)
		}
	}
}

func TestDeleteBucketRemovesBlobsAndUsage(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	var objects []repository.Object
	for key, body := range map[string]string{"a": "123", "b": "4567"} {
		obj, err := svc.Put(ctx, "", "drop", key, bytes.NewBufferString(body), int64(len(body)), PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, obj)
	}
	if err := svc.DeleteBucket(ctx, "", "drop"); err != nil {
		t.Fatal(err)
	}
	assertTenantUsage(t, repo, 0, 0)
	for _, obj := range objects {
		if _, err := svc.Storage().Stat(ctx, obj.StorageKey); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("bucket blob %q remains: %v", obj.StorageKey, err)
		}
	}
}

func TestGetVersionAppliesReadGuardsAndEmitsAccess(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	sink := &recordingSink{}
	svc.WithEventSink(sink)
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte("version body")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := svc.Put(ctx, "", "", "version.gz", bytes.NewReader(compressed.Bytes()), int64(compressed.Len()), PutOptions{
		ContentEncoding: "gzip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "", "", "version.gz", strings.NewReader("new"), 3, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	rc, _, err := svc.GetVersion(ctx, "", "", "version.gz", old.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(body, compressed.Bytes()) {
		t.Fatalf("version body differs from uploaded representation, err=%v", err)
	}
	if sink.events[len(sink.events)-1].Type != repository.EventAccessed {
		t.Fatalf("version read event = %q, want accessed", sink.events[len(sink.events)-1].Type)
	}
	corrupt, err := svc.Put(ctx, "", "", "corrupt-version", strings.NewReader("bad"), 3, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetObjectMetaKey(ctx, "default", "default", "corrupt-version", "_aero_scrub_status", "corrupt"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "", "", "corrupt-version", strings.NewReader("good"), 4, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetVersion(ctx, "", "", "corrupt-version", corrupt.VersionID); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("corrupt version read should fail, got %v", err)
	}
}

func assertTenantUsage(t *testing.T, repo repository.Repository, wantBytes, wantObjects int64) {
	t.Helper()
	usage, err := repo.GetTenantQuota(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if usage.UsedBytes != wantBytes || usage.UsedObjects != wantObjects {
		t.Fatalf("usage = %d bytes/%d objects, want %d/%d", usage.UsedBytes, usage.UsedObjects, wantBytes, wantObjects)
	}
}
