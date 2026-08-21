package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type localQuotaFallbackAccountant struct{}

func (localQuotaFallbackAccountant) CheckQuota(
	context.Context, string, repository.TenantQuota, int64, int64,
) error {
	return nil
}

func (localQuotaFallbackAccountant) Apply(
	context.Context, UsageMutation,
) (repository.TenantQuota, error) {
	return repository.TenantQuota{}, nil
}

func TestQuotaLocalBaselineEnforcedAfterAccountantDegrade(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	if err := repo.SetTenantQuota(ctx, DefaultTenant, 1, 0); err != nil {
		t.Fatalf("set local quota: %v", err)
	}
	svc.WithUsageAccountant(localQuotaFallbackAccountant{})
	_, err := svc.Put(ctx, DefaultTenant, DefaultBucket, "over-limit.txt",
		strings.NewReader("too large"), int64(len("too large")), PutOptions{})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("put error=%v, want local ErrQuotaExceeded after accountant degradation", err)
	}
}
