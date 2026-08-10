package repository

import (
	"context"
	"time"
)

const (
	AuditOriginAdmin               = "admin"
	AuditOriginFile                = "file"
	AuditGovernanceBindingActive   = "active"
	AuditGovernanceBindingDraining = "draining"
)

type AuditGovernanceBindingState struct {
	TenantID string
	State    string
}

// AuditGovernanceUnboundBacklogError exposes affected tenants only to the
// trusted runtime so it can replace them with keyed opaque references.
type AuditGovernanceUnboundBacklogError struct {
	tenantIDs []string
}

func (e *AuditGovernanceUnboundBacklogError) Error() string {
	return "audit governance desired bindings conflict with pending backlog"
}

func (e *AuditGovernanceUnboundBacklogError) TenantIDs() []string {
	return append([]string(nil), e.tenantIDs...)
}

// AuditGovernanceFact is the fixed, redacted record stored in the durable
// relay outbox. Raw audit details and object paths never enter this table.
type AuditGovernanceFact struct {
	ID              string
	TenantID        string
	OriginKind      string
	OriginID        int64
	FactKind        string
	ActorDigest     string
	Action          string
	TargetDigest    string
	RequestID       string
	DetailSHA256    string
	ObjectSizeBytes int64
	StorageBackend  string
	OccurredAt      time.Time
	Attempts        int
	ClaimOwner      string
	ClaimToken      string
	LeaseExpiresAt  time.Time
}

// AuditGovernanceGap is a local durable fact that has no relay outbox row yet.
// It exists only long enough for the relay to redact and enqueue it.
type AuditGovernanceGap struct {
	OriginKind string
	OriginID   int64
	TenantID   string
	Actor      string
	Action     string
	Target     string
	Detail     string
	RequestID  string
	Bucket     string
	Key        string
	Payload    map[string]string
	OccurredAt time.Time
}

// AuditGovernanceStore is deliberately separate from Repository so the
// optional integration does not widen the FileService persistence port.
type AuditGovernanceStore interface {
	ApplyAuditGovernanceBindings(context.Context, uint64, string, []AuditGovernanceBindingState) error
	AuditGovernanceCanDisable(context.Context) (bool, error)
	RecordAuditWithGovernance(context.Context, AuditEntry, AuditGovernanceFact) error
	InsertEventWithGovernance(context.Context, Event, AuditGovernanceFact) (int64, error)
	ListAuditGovernanceGaps(context.Context, string, int) ([]AuditGovernanceGap, error)
	EnqueueAuditGovernance(context.Context, AuditGovernanceFact) (bool, error)
	ClaimAuditGovernance(context.Context, string, string, uint64, int, time.Duration) ([]AuditGovernanceFact, error)
	CompleteAuditGovernance(context.Context, string, string, string) error
	RetryAuditGovernance(context.Context, string, string, string, string, time.Time) error
	OldestPendingAuditGovernance(context.Context) (time.Time, bool, error)
	HasPendingDrainingAuditGovernance(context.Context) (bool, error)
	CleanupDeliveredAuditGovernance(context.Context, time.Time, int) (int64, error)
	FailAuditGovernance(context.Context, string, string, string, string) error
	CleanupFailedAuditGovernance(context.Context, time.Time, int) (int64, error)
}

var _ AuditGovernanceStore = (*sqlStore)(nil)
