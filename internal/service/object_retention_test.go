package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMultipartDefaultRetentionIsPersisted(t *testing.T) {
	svc, _ := newQuotaTestSvc(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetBucketObjectLock(ctx, "", "", 60); err != nil {
		t.Fatal(err)
	}
	upload, err := svc.InitMultipart(
		ctx, "", "", "retained-multipart.bin", PutOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(
		ctx, upload.ID, 1, bytes.NewBufferString("body"), 4,
	); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.CompleteMultipart(ctx, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := svc.GetObjectRetention(
		ctx, "", "", saved.Key, saved.VersionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LockedUntil == nil ||
		saved.LockedUntil == nil ||
		!persisted.LockedUntil.Equal(*saved.LockedUntil) {
		t.Fatalf("persisted lock=%v, returned lock=%v",
			persisted.LockedUntil, saved.LockedUntil)
	}
	if err := svc.Delete(
		ctx, "", "", saved.Key, true,
	); !errors.Is(err, ErrLocked) {
		t.Fatalf("hard delete error=%v, want ErrLocked", err)
	}
}

func TestObjectRetentionTargetsExactVersionAndCannotShorten(t *testing.T) {
	svc, _ := newTestSvc(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "", "", "retained.txt", strings.NewReader("one"), 3, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := svc.Put(ctx, "", "", "retained.txt", strings.NewReader("two"), 3, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	got, err := svc.SetObjectRetention(ctx, "", "", "retained.txt", v1.VersionID, "compliance", until)
	if err != nil {
		t.Fatal(err)
	}
	if ObjectRetentionMode(got) != "COMPLIANCE" || got.LockedUntil == nil || !got.LockedUntil.Equal(until) {
		t.Fatalf("unexpected retention state: %+v", got)
	}
	current, err := svc.GetObjectRetention(ctx, "", "", "retained.txt", v2.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.LockedUntil != nil {
		t.Fatalf("retention leaked to current version: %+v", current.LockedUntil)
	}
	_, err = svc.SetObjectRetention(
		ctx, "", "", "retained.txt", v1.VersionID, "COMPLIANCE", until.Add(-time.Hour),
	)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("shortening retention error = %v, want ErrLocked", err)
	}
	if err := svc.DeleteVersion(ctx, "", "", "retained.txt", v1.VersionID); !errors.Is(err, ErrLocked) {
		t.Fatalf("delete retained version error = %v, want ErrLocked", err)
	}
}
