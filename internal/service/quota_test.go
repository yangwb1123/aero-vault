package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newQuotaTestSvc(t *testing.T) (*FileService, repository.Repository) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "obj")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	return NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{}), repo
}

// An unsized (no Content-Length) PUT must still be refused when the tenant is
// already at its byte cap — previously the `if size > 0` guard skipped quota
// entirely for size<=0, letting a chunked upload bypass the limit.
func TestPut_UnknownSizeRejectedAtByteCap(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if err := repo.SetTenantQuota(ctx, "default", 100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddTenantUsage(ctx, "default", 100, 1); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Put(ctx, "default", "", "k.txt", strings.NewReader("more"), -1, PutOptions{})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("unsized PUT at byte cap should be rejected, got %v", err)
	}
}

// And refused at the object cap, regardless of size.
func TestPut_UnknownSizeRejectedAtObjectCap(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if err := repo.SetTenantQuota(ctx, "default", 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddTenantUsage(ctx, "default", 10, 1); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Put(ctx, "default", "", "k.txt", strings.NewReader("x"), -1, PutOptions{})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("unsized PUT at object cap should be rejected, got %v", err)
	}
}

// Under the cap, an unsized PUT proceeds normally.
func TestPut_UnknownSizeAllowedUnderCap(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	if _, err := svc.Put(ctx, "default", "", "ok.txt", strings.NewReader("hello"), -1, PutOptions{}); err != nil {
		t.Fatalf("unsized PUT under cap should succeed, got %v", err)
	}
}

// A completed multipart upload must increment tenant usage — previously it wrote
// storage without ever accounting against the quota.
func TestCompleteMultipart_AccountsQuota(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	up, err := svc.InitMultipart(ctx, "default", "", "big.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part := []byte("hello multipart accounting")
	if _, err := svc.UploadPart(ctx, up.ID, 1, bytes.NewReader(part), int64(len(part))); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMultipart(ctx, up.ID); err != nil {
		t.Fatal(err)
	}
	q, _ := repo.GetTenantQuota(ctx, "default")
	if q.UsedBytes != int64(len(part)) || q.UsedObjects != 1 {
		t.Fatalf("multipart should account quota, got used=%d bytes / %d objects", q.UsedBytes, q.UsedObjects)
	}
}

// And a multipart upload that would exceed the byte cap is refused at completion.
func TestCompleteMultipart_RejectedAtByteCap(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if err := repo.SetTenantQuota(ctx, "default", 10, 0); err != nil {
		t.Fatal(err)
	}
	up, err := svc.InitMultipart(ctx, "default", "", "big.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part := []byte("this is definitely more than ten bytes")
	if _, err := svc.UploadPart(ctx, up.ID, 1, bytes.NewReader(part), int64(len(part))); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMultipart(ctx, up.ID); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("multipart over byte cap should be rejected, got %v", err)
	}
}
