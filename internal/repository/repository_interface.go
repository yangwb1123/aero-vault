package repository

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Repository is the metadata persistence contract. Implementations must be safe
// for concurrent use.
type Repository interface {
	Ping(ctx context.Context) error
	Close() error
	Migrate(ctx context.Context) error

	// ── Objects ──
	UpsertObject(ctx context.Context, obj Object) (Object, error)
	InsertObjectVersion(ctx context.Context, obj Object) (Object, error)
	GetObject(ctx context.Context, tenant, bucket, key string) (Object, error)
	GetObjectByID(ctx context.Context, id int64) (Object, error)
	GetObjectVersion(ctx context.Context, tenant, bucket, key, versionID string) (Object, error)
	ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (ListPage, error)
	ListObjectsByTag(ctx context.Context, tenant, bucket, prefix, marker string, limit int, tagKey, tagValue string) (ListPage, error)
	ListDeletedObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (ListPage, error)
	ListObjectVersions(ctx context.Context, tenant, bucket, key string) ([]Object, error)
	ListObjectVersionsWithOpts(ctx context.Context, tenant, bucket, key string, opts VersionListOpts) (VersionListPage, error)
	SoftDeleteObject(ctx context.Context, tenant, bucket, key string) error
	HardDeleteObject(ctx context.Context, tenant, bucket, key string) error
	HardDeleteObjectByID(ctx context.Context, id int64) error
	RestoreObject(ctx context.Context, tenant, bucket, key string) error
	UpdateTags(ctx context.Context, tenant, bucket, key string, tags map[string]string) error
	SetObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey, metaValue string) error
	SetObjectMetaKeys(ctx context.Context, tenant, bucket, key string, meta map[string]string) error
	ReplaceObjectMetadata(ctx context.Context, tenant, bucket, key string, meta map[string]string) error
	DeleteObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey string) error
	SetLockedUntil(ctx context.Context, tenant, bucket, key string, until time.Time) error
	StorageKeyReferenced(ctx context.Context, storageKey string) (bool, error)
	ListStorageKeys(ctx context.Context) ([]string, error)

	// ── Buckets ──
	CreateBucket(ctx context.Context, tenant, bucket string) error
	BucketExists(ctx context.Context, tenant, bucket string) (bool, error)
	ListBuckets(ctx context.Context, tenant string) ([]string, error)
	BucketStats(ctx context.Context, tenant, bucket string) (BucketStats, error)
	DeleteBucket(ctx context.Context, tenant, bucket string) error
	GetBucketConfig(ctx context.Context, tenant, bucket string) (BucketConfig, error)
	SetBucketVersioning(ctx context.Context, tenant, bucket string, enabled bool) error
	SetBucketObjectLock(ctx context.Context, tenant, bucket string, seconds int) error
	SetBucketLifecycle(ctx context.Context, tenant, bucket string, expireAfterDays int, expireAction string) error
	SetBucketNoncurrentVersionLifecycle(ctx context.Context, tenant, bucket string, noncurrentDays, noncurrentCount int) error
	SetBucketLifecycleFull(ctx context.Context, tenant, bucket string, lc LifecycleConfig) error
	ListTransitionable(ctx context.Context, limit int) ([]Object, error)
	SetBucketEncryption(ctx context.Context, tenant, bucket, algorithm, kmsKeyID string) error
	DeleteBucketEncryption(ctx context.Context, tenant, bucket string) error
	SetBucketACL(ctx context.Context, tenant, bucket, acl string) error
	SetBucketPolicy(ctx context.Context, tenant, bucket, policy string) error
	GetBucketCORS(ctx context.Context, tenant, bucket string) ([]CORSRule, error)
	SetBucketCORS(ctx context.Context, tenant, bucket string, rules []CORSRule) error
	DeleteBucketCORS(ctx context.Context, tenant, bucket string) error
	GetBucketLogging(ctx context.Context, tenant, bucket string) (LoggingConfig, error)
	SetBucketLogging(ctx context.Context, tenant, bucket, targetBucket, targetPrefix string) error
	DeleteBucketLogging(ctx context.Context, tenant, bucket string) error
	WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error
	GetBucketNotifications(ctx context.Context, tenant, bucket string) ([]NotificationRule, error)
	SetBucketNotifications(ctx context.Context, tenant, bucket string, rules []NotificationRule) error
	DeleteBucketNotifications(ctx context.Context, tenant, bucket string) error
	SetObjectACL(ctx context.Context, tenant, bucket, key, acl string) error
	GetObjectACL(ctx context.Context, tenant, bucket, key string) (string, error)
	ListExpired(ctx context.Context, limit int) ([]Object, error)
	ListSoftDeletedBefore(ctx context.Context, before string, limit int) ([]Object, error)
	ListExpiredNonCurrentVersions(ctx context.Context, limit int) ([]Object, error)

	// ── Multipart ──
	CreateUpload(ctx context.Context, u Upload) error
	GetUpload(ctx context.Context, uploadID string) (Upload, error)
	DeleteUpload(ctx context.Context, uploadID string) error
	ListUploads(ctx context.Context, tenant, bucket, keyMarker, uploadIDMarker string, limit int) ([]Upload, error)
	RecordPart(ctx context.Context, p PartRecord) error
	ListParts(ctx context.Context, uploadID string) ([]PartRecord, error)

	// ── Events ──
	InsertEvent(ctx context.Context, e Event) (int64, error)
	NextUnconsumedEvents(ctx context.Context, limit int) ([]Event, error)
	MarkEventConsumed(ctx context.Context, id int64) error

	// ── AI Chunks ──
	DeleteChunksForObject(ctx context.Context, objectID int64) error
	InsertChunks(ctx context.Context, chunks []Chunk) error
	ListChunksForObject(ctx context.Context, objectID int64) ([]Chunk, error)
	SearchChunks(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]SearchHit, error)
	ListObjectIDsToReindex(ctx context.Context, tenant, currentModel string, limit int) ([]int64, error)

	// ── AI Usage ──
	RecordUsage(ctx context.Context, u Usage) error
	ListUsageForObject(ctx context.Context, tenant string, objectID int64, limit int) ([]Usage, error)
	SumAICostMicros(ctx context.Context, tenant, since string) (int64, error)
	StorageClassCounts(ctx context.Context, tenant string) (map[string]int64, error)

	// ── Quota ──
	GetTenantQuota(ctx context.Context, tenant string) (TenantQuota, error)
	ListTenantQuotas(ctx context.Context) ([]TenantQuota, error)
	SetTenantQuota(ctx context.Context, tenant string, maxBytes, maxObjects int64) error
	SetTenantBudgetMicros(ctx context.Context, tenant string, micros int64) error
	AddTenantUsage(ctx context.Context, tenant string, deltaBytes, deltaObjects int64) (TenantQuota, error)

	// ── Webhooks ──
	RecordWebhookFailure(ctx context.Context, f WebhookFailure) (int64, error)
	NextPendingFailures(ctx context.Context, limit int) ([]WebhookFailure, error)
	MarkWebhookSucceeded(ctx context.Context, id int64) error
	UpdateWebhookFailure(ctx context.Context, id int64, lastErr string, lastStatus int, nextRetryAt time.Time, attempts int) error
	ListWebhookFailures(ctx context.Context, limit int) ([]WebhookFailure, error)

	// ── Jobs ──
	EnqueueJob(ctx context.Context, j Job) (id int64, deduped bool, err error)
	ClaimJob(ctx context.Context, worker string) (Job, bool, error)
	CompleteJob(ctx context.Context, id int64, result string) error
	RetryJob(ctx context.Context, id int64, lastErr string, runAfter time.Time) error
	FailJob(ctx context.Context, id int64, lastErr string) error
	ListJobs(ctx context.Context, status, jobType string, limit int) ([]Job, error)
	CountJobsByStatus(ctx context.Context, status string) (int, error)
	JobStats(ctx context.Context) (map[string]int64, error)
	ReapStuckJobs(ctx context.Context, maxAge time.Duration) (int64, error)

	// ── Idempotency ──
	ClaimIdempotencyKey(ctx context.Context, tenant, key, fingerprint, requestID string) (rec IdempotencyRecord, claimed bool, err error)
	CompleteIdempotencyKey(ctx context.Context, tenant, key string, status int, body []byte, contentType string, headers map[string][]string) error
	DeleteIdempotencyKey(ctx context.Context, tenant, key string) error
	DeleteIdempotencyKeysBefore(ctx context.Context, before string) (int64, error)

	// ── API Keys ──
	PutAPIKey(ctx context.Context, k APIKeyRecord) error
	GetAPIKeyByHash(ctx context.Context, hash string) (APIKeyRecord, bool, error)
	DeleteAPIKeyByHash(ctx context.Context, hash string) (bool, error)
	ListAPIKeys(ctx context.Context, tenant string) ([]APIKeyRecord, error)
	TouchAPIKey(ctx context.Context, hash, when string) error

	// ── Distributed leases ──
	AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)

	// ── Tenants ──
	UpsertTenant(ctx context.Context, tr TenantRecord) error
	GetTenant(ctx context.Context, tenantID string) (TenantRecord, bool, error)
	ListTenants(ctx context.Context) ([]TenantRecord, error)
	DeleteTenant(ctx context.Context, tenantID string) (bool, error)

	// ── Audit ──
	RecordAudit(ctx context.Context, e AuditEntry) error
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)

	// ── Legal holds ──
	PutLegalHold(ctx context.Context, l LegalHold) error
	GetLegalHold(ctx context.Context, objectID int64, versionID string) (LegalHold, error)
	ListLegalHolds(ctx context.Context, objectID int64) ([]LegalHold, error)
	RemoveLegalHold(ctx context.Context, objectID int64, versionID string) error
	ObjectHasLegalHold(ctx context.Context, objectID int64) (bool, error)
	ListObjectsOnLegalHold(ctx context.Context, tenant string, limit int) ([]int64, error)

	// ── Upload GC ──
	ListExpiredUploads(ctx context.Context, before string, limit int) ([]Upload, error)
	DeleteUploadCascade(ctx context.Context, uploadID string) error
	ListZombieUploads(ctx context.Context, before string, limit int) ([]Upload, error)
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
