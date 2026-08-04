package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestPutRejectsLockedOverwriteBeforeStorageMutation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "locked-overwrite.txt", "original")
	if err := svc.LockObject(ctx, "", "", "locked-overwrite.txt", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Put(ctx, "", "", "locked-overwrite.txt", strings.NewReader("replacement"), 11, PutOptions{})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	assertObjectBody(t, svc, "locked-overwrite.txt", "original")
}

func TestPutRejectsLegalHoldOverwriteBeforeStorageMutation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "held-overwrite.txt", "original")
	if err := svc.PutLegalHold(ctx, "", "", "held-overwrite.txt", "", "case", "tester"); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Put(ctx, "", "", "held-overwrite.txt", strings.NewReader("replacement"), 11, PutOptions{})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	assertObjectBody(t, svc, "held-overwrite.txt", "original")
}

func TestCompleteMultipartRejectsLegalHoldOverwrite(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "held-multipart.txt", "original")
	upload, err := svc.InitMultipart(ctx, "", "", "held-multipart.txt", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(ctx, upload.ID, 1, strings.NewReader("replacement"), 11); err != nil {
		t.Fatal(err)
	}
	if err := svc.PutLegalHold(ctx, "", "", "held-multipart.txt", "", "case", "tester"); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CompleteMultipart(ctx, upload.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	assertObjectBody(t, svc, "held-multipart.txt", "original")
}

func TestHardDeleteChecksProtectedHistoricalVersion(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestSvc(t)
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	first := putTestObject(t, svc, "history.txt", "version-one")
	if err := repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: first.ID, TenantID: first.TenantID, VersionID: first.VersionID,
		HoldReason: "case", CreatedBy: "tester",
	}); err != nil {
		t.Fatal(err)
	}
	putTestObject(t, svc, "history.txt", "version-two")

	if err := svc.Delete(ctx, "", "", "history.txt", true); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	assertObjectBody(t, svc, "history.txt", "version-two")
}

func TestDeleteBucketRejectsProtectedObject(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "bucket-lock.txt", "content")
	if err := svc.LockObject(ctx, "", "", "bucket-lock.txt", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteBucket(ctx, "", ""); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	exists, err := svc.HeadBucket(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("protected bucket must remain")
	}
}

func assertObjectBody(t *testing.T, svc *FileService, key, want string) {
	t.Helper()
	rc, _, err := svc.Get(context.Background(), "", "", key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
