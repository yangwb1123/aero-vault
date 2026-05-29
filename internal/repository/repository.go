package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound       = errors.New("object not found")
	ErrDuplicate      = errors.New("object already exists")
	ErrUploadNotFound = errors.New("upload not found")
)

// Object is the persisted view of a stored object.
type Object struct {
	ID          int64
	TenantID    string
	Bucket      string
	Key         string
	VersionID   string // set per-insert; stable for the life of the row
	Backend     string
	StorageKey  string
	Size        int64
	ETag        string
	ContentType string
	Metadata    map[string]string
	Tags        map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
	LockedUntil *time.Time // present when Object Lock is active
}

// BucketConfig is the per-bucket policy bundle (versioning + lock + lifecycle).
type BucketConfig struct {
	TenantID          string
	Name              string
	Versioning        bool
	ObjectLockSeconds int
	ExpireAfterDays   int
	ExpireAction      string // "soft_delete" | "hard_delete"
	ACL               string // canned ACL: private | public-read | public-read-write | authenticated-read
}

// ListPage is a paginated slice of Object rows.
type ListPage struct {
	Objects    []Object
	NextMarker string
	HasMore    bool
}

// Upload tracks a multipart upload session by upload ID.
type Upload struct {
	ID         string
	TenantID   string
	Bucket     string
	Key        string
	Backend    string
	BackendUID string
	Metadata   map[string]string
	CreatedAt  time.Time
}

// PartRecord is a part that has been uploaded.
type PartRecord struct {
	UploadID   string
	PartNumber int32
	ETag       string
	Size       int64
}

// EventType enumerates lifecycle events emitted by the FileService.
type EventType string

const (
	EventCreated  EventType = "created"
	EventDeleted  EventType = "deleted"
	EventAccessed EventType = "accessed"
)

// Event is a persisted lifecycle event awaiting consumption.
type Event struct {
	ID        int64
	TenantID  string
	Bucket    string
	Key       string
	Type      EventType
	ObjectID  *int64
	RequestID string
	Payload   map[string]string
	CreatedAt time.Time
}

// Chunk is a text slice extracted from an object, optionally with an embedding.
type Chunk struct {
	ID         int64
	ObjectID   int64
	TenantID   string
	Bucket     string
	ObjectKey  string
	Seq        int
	Content    string
	Embedding  []float32
	Dim        int
	EmbedModel string
	CreatedAt  time.Time
}

// Usage records that an AI caller looked up specific chunks/objects.
type Usage struct {
	ID        int64
	TenantID  string
	Caller    string
	Query     string
	ChunkIDs  []int64
	ObjectIDs []int64
	RequestID string
	CreatedAt time.Time

	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMs        int64
	CostMicros       int64
}

// SearchHit is one ranked chunk returned by Repository.SearchChunks.
type SearchHit struct {
	Chunk Chunk
	Score float32
}

// IdempotencyRecord is a persisted request-deduplication entry keyed by
// (tenant, key). A claim starts 'in_progress'; once the original request
// finishes its response is captured so retries can replay it.
type IdempotencyRecord struct {
	TenantID       string
	Key            string
	Fingerprint    string
	Status         string // "in_progress" | "completed"
	ResponseStatus int
	ResponseBody   []byte
	ResponseCT     string
	RequestID      string
	CreatedAt      string
	CompletedAt    string
}

// APIKeyRecord is a persisted, hashed API-key entry. The token itself is never
// stored; TokenHash is the caller-supplied sha256 hex of the token.
type APIKeyRecord struct {
	TokenHash  string
	TenantID   string
	Scopes     string
	Label      string
	CreatedAt  string
	ExpiresAt  string
	LastUsedAt string
}

// TenantRecord is a persisted tenant, letting an operator onboard or disable
// tenants at runtime without a redeploy.
type TenantRecord struct {
	TenantID    string
	DisplayName string
	Status      string
	CreatedAt   string
}

// AuditEntry is a persisted record of an admin/security-sensitive action: who
// did what, to which target, with optional freeform detail.
type AuditEntry struct {
	ID        int64
	CreatedAt string
	Actor     string
	Action    string
	Target    string
	TenantID  string
	Detail    string
}

// Repository persists object metadata, multipart state, events, chunks, and audit.
type Repository interface {
	Ping(ctx context.Context) error
	Close() error
	Migrate(ctx context.Context) error

	UpsertObject(ctx context.Context, obj Object) (Object, error)
	InsertObjectVersion(ctx context.Context, obj Object) (Object, error)
	GetObject(ctx context.Context, tenant, bucket, key string) (Object, error)
	GetObjectByID(ctx context.Context, id int64) (Object, error)
	GetObjectVersion(ctx context.Context, tenant, bucket, key, versionID string) (Object, error)
	ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (ListPage, error)
	ListObjectVersions(ctx context.Context, tenant, bucket, key string) ([]Object, error)
	SoftDeleteObject(ctx context.Context, tenant, bucket, key string) error
	HardDeleteObject(ctx context.Context, tenant, bucket, key string) error
	UpdateTags(ctx context.Context, tenant, bucket, key string, tags map[string]string) error
	SetLockedUntil(ctx context.Context, tenant, bucket, key string, until time.Time) error
	StorageKeyReferenced(ctx context.Context, storageKey string) (bool, error)

	CreateBucket(ctx context.Context, tenant, bucket string) error
	BucketExists(ctx context.Context, tenant, bucket string) (bool, error)
	ListBuckets(ctx context.Context, tenant string) ([]string, error)
	GetBucketConfig(ctx context.Context, tenant, bucket string) (BucketConfig, error)
	SetBucketVersioning(ctx context.Context, tenant, bucket string, enabled bool) error
	SetBucketObjectLock(ctx context.Context, tenant, bucket string, seconds int) error
	SetBucketLifecycle(ctx context.Context, tenant, bucket string, expireAfterDays int, expireAction string) error
	SetBucketACL(ctx context.Context, tenant, bucket, acl string) error
	SetObjectACL(ctx context.Context, tenant, bucket, key, acl string) error
	GetObjectACL(ctx context.Context, tenant, bucket, key string) (string, error)
	ListExpired(ctx context.Context, limit int) ([]Object, error)
	ListSoftDeletedBefore(ctx context.Context, before string, limit int) ([]Object, error)

	CreateUpload(ctx context.Context, u Upload) error
	GetUpload(ctx context.Context, uploadID string) (Upload, error)
	DeleteUpload(ctx context.Context, uploadID string) error
	ListUploads(ctx context.Context, tenant, bucket string, limit int) ([]Upload, error)

	RecordPart(ctx context.Context, p PartRecord) error
	ListParts(ctx context.Context, uploadID string) ([]PartRecord, error)

	// Events
	InsertEvent(ctx context.Context, e Event) (int64, error)
	NextUnconsumedEvents(ctx context.Context, limit int) ([]Event, error)
	MarkEventConsumed(ctx context.Context, id int64) error

	// Chunks
	DeleteChunksForObject(ctx context.Context, objectID int64) error
	InsertChunks(ctx context.Context, chunks []Chunk) error
	ListChunksForObject(ctx context.Context, objectID int64) ([]Chunk, error)
	SearchChunks(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]SearchHit, error)
	ListObjectIDsToReindex(ctx context.Context, tenant, currentModel string, limit int) ([]int64, error)

	// Audit
	RecordUsage(ctx context.Context, u Usage) error
	ListUsageForObject(ctx context.Context, tenant string, objectID int64, limit int) ([]Usage, error)
	// SumAICostMicros totals recorded AI cost (USD-millionths) for a tenant since
	// the given RFC3339 timestamp — used to enforce per-tenant budgets.
	SumAICostMicros(ctx context.Context, tenant, since string) (int64, error)

	// Quota
	GetTenantQuota(ctx context.Context, tenant string) (TenantQuota, error)
	ListTenantQuotas(ctx context.Context) ([]TenantQuota, error)
	SetTenantQuota(ctx context.Context, tenant string, maxBytes, maxObjects int64) error
	AddTenantUsage(ctx context.Context, tenant string, deltaBytes, deltaObjects int64) (TenantQuota, error)

	// Webhook retries
	RecordWebhookFailure(ctx context.Context, f WebhookFailure) (int64, error)
	NextPendingFailures(ctx context.Context, limit int) ([]WebhookFailure, error)
	MarkWebhookSucceeded(ctx context.Context, id int64) error
	UpdateWebhookFailure(ctx context.Context, id int64, lastErr string, lastStatus int, nextRetryAt time.Time, attempts int) error
	ListWebhookFailures(ctx context.Context, limit int) ([]WebhookFailure, error)

	// Background job queue
	EnqueueJob(ctx context.Context, j Job) (id int64, deduped bool, err error)
	ClaimJob(ctx context.Context, worker string) (Job, bool, error)
	CompleteJob(ctx context.Context, id int64, result string) error
	RetryJob(ctx context.Context, id int64, lastErr string, runAfter time.Time) error
	FailJob(ctx context.Context, id int64, lastErr string) error
	ListJobs(ctx context.Context, status, jobType string, limit int) ([]Job, error)
	CountJobsByStatus(ctx context.Context, status string) (int, error)
	JobStats(ctx context.Context) (map[string]int64, error)
	ReapStuckJobs(ctx context.Context, maxAge time.Duration) (int64, error)

	// Idempotency keys
	ClaimIdempotencyKey(ctx context.Context, tenant, key, fingerprint, requestID string) (rec IdempotencyRecord, claimed bool, err error)
	CompleteIdempotencyKey(ctx context.Context, tenant, key string, status int, body []byte, contentType string) error
	DeleteIdempotencyKey(ctx context.Context, tenant, key string) error
	DeleteIdempotencyKeysBefore(ctx context.Context, before string) (int64, error)

	// API keys (stored only as sha256 hashes)
	PutAPIKey(ctx context.Context, k APIKeyRecord) error
	GetAPIKeyByHash(ctx context.Context, hash string) (APIKeyRecord, bool, error)
	DeleteAPIKeyByHash(ctx context.Context, hash string) (bool, error)
	ListAPIKeys(ctx context.Context, tenant string) ([]APIKeyRecord, error)
	TouchAPIKey(ctx context.Context, hash, when string) error

	// Distributed leases (singleton coordination across replicas)
	AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)

	// Tenants (runtime onboarding / disabling)
	UpsertTenant(ctx context.Context, tr TenantRecord) error
	GetTenant(ctx context.Context, tenantID string) (TenantRecord, bool, error)
	ListTenants(ctx context.Context) ([]TenantRecord, error)
	DeleteTenant(ctx context.Context, tenantID string) (bool, error)

	// Audit log (admin/security actions)
	RecordAudit(ctx context.Context, e AuditEntry) error
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)
}

func Open(ctx context.Context, driver, dsn string) (Repository, error) {
	switch strings.ToLower(driver) {
	case "postgres":
		return openPostgres(ctx, dsn)
	case "sqlite":
		return openSQLite(ctx, dsn)
	default:
		return nil, fmt.Errorf("unknown driver %q", driver)
	}
}
