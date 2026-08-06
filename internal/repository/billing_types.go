package repository

import (
	"context"
	"time"
)

const (
	BillingDimensionBytesAllocated = "storage_bytes_allocated"
	BillingDimensionBytesReclaimed = "storage_bytes_reclaimed"
	BillingDimensionObjectsCreated = "storage_objects_created"
	BillingDimensionObjectsDeleted = "storage_objects_deleted"
)

type BillingLimit struct {
	Hard      int64
	Unlimited bool
}

type BillingProjection struct {
	TenantID    string
	Revision    uint64
	Active      bool
	Bytes       BillingLimit
	Objects     BillingLimit
	EffectiveAt time.Time
	ExpiresAt   time.Time
	ProjectedAt time.Time
}

type BillingUsageMutation struct {
	OperationID  string
	TenantID     string
	Kind         string
	DeltaBytes   int64
	DeltaObjects int64
	OccurredAt   time.Time
}

type BillingUsageFact struct {
	ID           string
	OperationID  string
	TenantID     string
	Dimension    string
	Quantity     int64
	OccurredAt   time.Time
	MetadataJSON string
	Attempts     int
	ClaimOwner   string
}

// BillingStore is the focused persistence contract used by the optional
// Snaplink integration. It intentionally stays separate from Repository so
// deployments that do not enable billing keep the existing FileService port.
type BillingStore interface {
	GetBillingProjection(ctx context.Context, tenant string) (BillingProjection, bool, error)
	ApplyBillingProjection(ctx context.Context, projection BillingProjection) (bool, error)
	ApplyBillingUsage(ctx context.Context, mutation BillingUsageMutation) (TenantQuota, bool, error)
	ClaimBillingUsage(ctx context.Context, owner string, limit int, ttl time.Duration) ([]BillingUsageFact, error)
	CompleteBillingUsage(ctx context.Context, id, owner string) error
	RetryBillingUsage(ctx context.Context, id, owner, lastErr string, next time.Time) error
}

var _ BillingStore = (*sqlStore)(nil)
