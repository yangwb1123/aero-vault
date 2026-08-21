package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPreparePresignPutRejectsFullTenantQuota(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if err := repo.SetTenantQuota(ctx, "default", 10, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddTenantUsage(ctx, "default", 10, 1); err != nil {
		t.Fatal(err)
	}

	err := svc.PreparePresignPut(ctx, "default", "", "new.txt", time.Minute)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("presigned PUT at tenant quota should be rejected, got %v", err)
	}
}

func TestPreparePresignPutAllowsOverwriteAtObjectCap(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if _, err := svc.Put(ctx, "default", "", "same.txt", strings.NewReader("old"), 3, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetTenantQuota(ctx, "default", 100, 1); err != nil {
		t.Fatal(err)
	}

	if err := svc.PreparePresignPut(ctx, "default", "", "same.txt", time.Minute); err != nil {
		t.Fatalf("overwrite at object cap should be allowed, got %v", err)
	}
}

func TestPreparePresignPutHonorsBucketQuota(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	if _, err := svc.Put(ctx, "default", "", "same.txt", strings.NewReader("old"), 3, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetBucketQuota(ctx, "default", "", 3, 0); err != nil {
		t.Fatal(err)
	}

	err := svc.PreparePresignPut(ctx, "default", "", "new.txt", time.Minute)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("presigned PUT at bucket byte quota should be rejected, got %v", err)
	}
}
