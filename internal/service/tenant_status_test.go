package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestTenantStatusEnforcementAtServiceBoundary(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestSvc(t)
	if err := repo.UpsertTenant(ctx, repository.TenantRecord{
		TenantID: "acme", DisplayName: "Acme", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	svc.WithTenantStatusEnforcement()
	if _, err := svc.Put(
		ctx, "acme", "default", "report.txt", strings.NewReader("ok"), 2, PutOptions{},
	); err != nil {
		t.Fatalf("active tenant put: %v", err)
	}
	if err := repo.UpsertTenant(ctx, repository.TenantRecord{
		TenantID: "acme", DisplayName: "Acme", Status: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Stat(ctx, "acme", "default", "report.txt")
	assertTenantDisabled(t, err)
	if _, err := svc.List(ctx, "acme", "default", "", "", 100); !errors.Is(err, ErrTenantDisabled) {
		t.Fatalf("disabled tenant list error=%v", err)
	}
	if _, err := svc.ListBuckets(ctx, "acme"); !errors.Is(err, ErrTenantDisabled) {
		t.Fatalf("disabled tenant bucket list error=%v", err)
	}
	if _, err := svc.Usage(ctx, "acme"); !errors.Is(err, ErrTenantDisabled) {
		t.Fatalf("disabled tenant usage error=%v", err)
	}
	system := access.SystemContext(ctx, "acme")
	if _, err := svc.Stat(system, "acme", "default", "report.txt"); err != nil {
		t.Fatalf("system cleanup principal should bypass tenant suspension: %v", err)
	}
}

func assertTenantDisabled(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrForbidden) || !errors.Is(err, ErrTenantDisabled) {
		t.Fatalf("error=%v, want ErrForbidden + ErrTenantDisabled", err)
	}
}

func TestTenantStatusUnknownTenantCompatibility(t *testing.T) {
	svc, _ := newTestSvc(t)
	svc.WithTenantStatusEnforcement()
	if _, err := svc.Put(
		context.Background(), "implicit", "default", "new.txt",
		strings.NewReader("ok"), 2, PutOptions{},
	); err != nil {
		t.Fatalf("unknown implicit tenant should remain allowed: %v", err)
	}
}
