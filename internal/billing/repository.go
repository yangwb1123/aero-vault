package billing

import (
	"context"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

const usageRepositoryAdjustment = "repository_adjustment"

type meteredRepository struct {
	repository.Repository
	runtime *Runtime
}

// WrapRepository preserves the existing broad persistence port while routing
// maintenance-path usage corrections (reconcile/lifecycle) through the same
// atomic local-gauge + billing-outbox transaction as FileService.
func WrapRepository(repo repository.Repository, runtime *Runtime) repository.Repository {
	if runtime == nil {
		return repo
	}
	return &meteredRepository{Repository: repo, runtime: runtime}
}

func (r *meteredRepository) AddTenantUsage(
	ctx context.Context, tenant string, deltaBytes, deltaObjects int64,
) (repository.TenantQuota, error) {
	return r.runtime.Apply(ctx, service.UsageMutation{
		TenantID: tenant, Kind: usageRepositoryAdjustment,
		DeltaBytes: deltaBytes, DeltaObjects: deltaObjects,
		OccurredAt: time.Now().UTC(),
	})
}

var _ repository.Repository = (*meteredRepository)(nil)
