package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestMultipartCompletionUsesExactClientManifest(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	upload, err := svc.InitMultipart(ctx, "", "", "subset.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(
		ctx, upload.ID, 1, strings.NewReader("excluded"), 8,
	); err != nil {
		t.Fatal(err)
	}
	selected, err := svc.UploadPart(
		ctx, upload.ID, 2, strings.NewReader("included"), 8,
	)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := svc.CompleteMultipartWithParts(ctx, upload.ID, []repository.PartRecord{
		{PartNumber: selected.PartNumber, ETag: selected.ETag},
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Size != 8 {
		t.Fatalf("completed size = %d, want 8", obj.Size)
	}
	rc, _, err := svc.Get(ctx, "", "", "subset.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if string(body) != "included" {
		t.Fatalf("completed body = %q, want exact selected part", body)
	}
}

func TestMultipartPartCanBeReplaced(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	upload, err := svc.InitMultipart(ctx, "", "", "replace.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(ctx, upload.ID, 1, strings.NewReader("old"), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(ctx, upload.ID, 1, strings.NewReader("new"), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMultipart(ctx, upload.ID); err != nil {
		t.Fatal(err)
	}
	rc, _, err := svc.Get(ctx, "", "", "replace.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if string(body) != "new" {
		t.Fatalf("replacement part body = %q, want new", body)
	}
}

func TestMultipartCompletionReplaySurvivesUploadDeletion(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	upload, err := svc.InitMultipart(ctx, "", "", "replay.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(
		ctx, upload.ID, 1, bytes.NewBufferString("content"), 7,
	); err != nil {
		t.Fatal(err)
	}
	first, err := svc.CompleteMultipart(ctx, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := svc.CompleteMultipart(ctx, upload.ID)
	if err != nil {
		t.Fatalf("completion replay: %v", err)
	}
	if replayed.ID != first.ID || replayed.VersionID != first.VersionID {
		t.Fatalf("replayed object = %+v, want original %+v", replayed, first)
	}
}

func TestMultipartScopePreventsCrossTenantAndKeyAccess(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	upload, err := svc.InitMultipart(ctx, "victim", "private", "secret.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPartFor(
		ctx, MultipartScope{TenantID: "attacker"},
		upload.ID, 1, strings.NewReader("x"), 1, ReadOptions{},
	); !errors.Is(err, ErrUploadNotFound) {
		t.Fatalf("cross-tenant upload error = %v, want ErrUploadNotFound", err)
	}
	scope := MultipartScope{TenantID: "victim", Bucket: "private", Key: "secret.bin"}
	part, err := svc.UploadPartFor(
		ctx, scope, upload.ID, 1, strings.NewReader("ok"), 2, ReadOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey := MultipartScope{TenantID: "victim", Bucket: "private", Key: "other.bin"}
	if _, err := svc.CompleteMultipartWithPartsFor(
		ctx, wrongKey, upload.ID,
		[]repository.PartRecord{{PartNumber: 1, ETag: part.ETag}}, ReadOptions{},
	); !errors.Is(err, ErrUploadNotFound) {
		t.Fatalf("wrong-key completion error = %v, want ErrUploadNotFound", err)
	}
	if _, err := svc.CompleteMultipartWithPartsFor(
		ctx, scope, upload.ID,
		[]repository.PartRecord{{PartNumber: 1, ETag: part.ETag}}, ReadOptions{},
	); err != nil {
		t.Fatalf("correct scoped completion: %v", err)
	}
}

func TestMultipartManifestMustBeStrictlyAscending(t *testing.T) {
	stored := []repository.PartRecord{
		{PartNumber: 1, ETag: "one"},
		{PartNumber: 2, ETag: "two"},
	}
	_, err := selectMultipartParts([]repository.PartRecord{
		{PartNumber: 2, ETag: "two"},
		{PartNumber: 1, ETag: "one"},
	}, stored)
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("descending manifest error = %v, want ErrInvalidArgs", err)
	}
}

func TestUploadPartCopyValidatesRangeAndAccountsExactBytes(t *testing.T) {
	svc, _ := newTestSvc(t)
	ctx := context.Background()
	putTestObject(t, svc, "source.bin", "abcdef")
	upload, err := svc.InitMultipart(ctx, "", "", "copy.bin", PutOptions{})
	if err != nil {
		t.Fatalf("init multipart: %v", err)
	}

	if _, err := svc.UploadPartCopy(
		ctx, upload.ID, 0, "source.bin", 0, 1,
	); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("invalid part number error = %v, want ErrInvalidArgs", err)
	}
	if _, err := svc.UploadPartCopy(
		ctx, upload.ID, 1, "source.bin", 4, 3,
	); !errors.Is(err, ErrRangeNotSatisfiable) {
		t.Fatalf("out-of-range copy error = %v, want ErrRangeNotSatisfiable", err)
	}
	part, err := svc.UploadPartCopy(ctx, upload.ID, 1, "source.bin", 1, 3)
	if err != nil {
		t.Fatalf("copy part: %v", err)
	}
	if part.Size != 3 {
		t.Fatalf("copied part size = %d, want 3", part.Size)
	}
	obj, err := svc.CompleteMultipart(ctx, upload.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if obj.Size != 3 {
		t.Fatalf("completed object size = %d, want 3", obj.Size)
	}
	assertObjectBody(t, svc, "copy.bin", "bcd")
}

func TestUploadPartCopyDecryptsLocalEncryptedSourceBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(
		ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "copy-sse.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	store, err := storage.NewLocal(storage.LocalConfig{
		Root:   filepath.Join(t.TempDir(), "objects"),
		SSEKey: "server-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewFileService(store, repo, nil)
	putTestObject(t, svc, "source.bin", "plaintext")

	upload, err := svc.InitMultipart(ctx, "", "", "copy.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part, err := svc.UploadPartCopy(ctx, upload.ID, 1, "source.bin", -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMultipartWithParts(
		ctx, upload.ID, []repository.PartRecord{part},
	); err != nil {
		t.Fatal(err)
	}
	assertObjectBody(t, svc, "copy.bin", "plaintext")
}

func TestUploadPartRejectsSizeMismatchWithoutRecordingPart(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestSvc(t)
	upload, err := svc.InitMultipart(ctx, "", "", "mismatch.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(
		ctx, upload.ID, 1, strings.NewReader("short"), 6,
	); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("part error = %v, want ErrSizeMismatch", err)
	}
	parts, err := repo.ListParts(ctx, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 0 {
		t.Fatalf("recorded mismatched parts = %+v, want none", parts)
	}

	part, err := svc.UploadPart(ctx, upload.ID, 1, strings.NewReader("valid"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if part.Size != 5 {
		t.Fatalf("retried part size = %d, want 5", part.Size)
	}
	if _, err := svc.CompleteMultipart(ctx, upload.ID); err != nil {
		t.Fatal(err)
	}
	assertObjectBody(t, svc, "mismatch.bin", "valid")
}

func TestUploadPartUnknownSizeRecordsActualBytes(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	upload, err := svc.InitMultipart(ctx, "", "", "unknown-part.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part, err := svc.UploadPart(ctx, upload.ID, 1, strings.NewReader("actual"), -1)
	if err != nil {
		t.Fatal(err)
	}
	if part.Size != 6 {
		t.Fatalf("part size = %d, want 6", part.Size)
	}
	if _, err := svc.CompleteMultipart(ctx, upload.ID); err != nil {
		t.Fatal(err)
	}
	assertObjectBody(t, svc, "unknown-part.bin", "actual")
}
