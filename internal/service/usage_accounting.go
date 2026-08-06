package service

import (
	"context"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

const (
	UsageObjectWrite   = "object_write"
	UsageObjectDelete  = "object_delete"
	UsageObjectRestore = "object_restore"
	UsageBucketDelete  = "bucket_delete"
	UsageQuarantine    = "object_quarantine"
)

type UsageMutation struct {
	TenantID     string
	Kind         string
	DeltaBytes   int64
	DeltaObjects int64
	OccurredAt   time.Time
}

// UsageAccountant is the optional commercial quota boundary. CheckQuota must
// use only durable local projections; Apply must update local used counters and
// enqueue immutable usage facts atomically.
type UsageAccountant interface {
	CheckQuota(ctx context.Context, tenant string, current repository.TenantQuota, deltaBytes, deltaObjects int64) error
	Apply(ctx context.Context, mutation UsageMutation) (repository.TenantQuota, error)
}

func (s *FileService) addTenantUsage(
	ctx context.Context, tenant, kind string, deltaBytes, deltaObjects int64,
) (repository.TenantQuota, error) {
	if s.usageAccountant == nil {
		return s.repo.AddTenantUsage(ctx, tenant, deltaBytes, deltaObjects)
	}
	return s.usageAccountant.Apply(ctx, UsageMutation{
		TenantID: tenant, Kind: kind, DeltaBytes: deltaBytes,
		DeltaObjects: deltaObjects, OccurredAt: time.Now().UTC(),
	})
}
