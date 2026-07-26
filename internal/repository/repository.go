package repository

import (
	"errors"
	"time"
)

var (
	ErrNotFound        = errors.New("object not found")
	ErrDuplicate       = errors.New("object already exists")
	ErrUploadNotFound  = errors.New("upload not found")
	ErrLegalHoldActive = errors.New("object is under legal hold and cannot be deleted")
)

// Object is the persisted view of a stored object.
type Object struct {
	ID               int64
	TenantID         string
	Bucket           string
	Key              string
	VersionID        string
	Backend          string
	StorageKey       string
	Size             int64
	ETag             string
	ContentType      string
	Metadata         map[string]string
	Tags             map[string]string
	StorageClass     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
	LockedUntil      *time.Time
	VersionTombstone bool
}

// BucketConfig is the per-bucket policy bundle.
// TransitionRule defines a lifecycle transition: move to storage_class after
// the specified number of days from object creation.
type TransitionRule struct {
	Days         int    `json:"days"`
	StorageClass string `json:"storage_class"`
}

// BucketConfig holds the full configuration state of a single bucket.
// SSEAlgorithm and SSEKMSKeyId control per-bucket server-side encryption.
type BucketConfig struct {
	TenantID                         string
	Name                             string
	Versioning                       bool
	ObjectLockSeconds                int
	ExpireAfterDays                  int
	ExpireAction                     string
	NoncurrentDays                   int
	NoncurrentCount                  int
	ACL                              string
	Policy                           string
	CORSRules                        []CORSRule
	LoggingTarget                    string
	LoggingPrefix                    string
	NotificationRules                []NotificationRule
	SSEAlgorithm                     string           `json:"sse_algorithm,omitempty"` // "" | "AES256" | "aws:kms"
	SSEKMSKeyId                      string           `json:"sse_kms_key_id,omitempty"`
	TransitionRules                  []TransitionRule `json:"transition_rules,omitempty"`
	NoncurrentTransitionDays         int              `json:"noncurrent_transition_days,omitempty"`
	NoncurrentTransitionStorageClass string           `json:"noncurrent_transition_storage_class,omitempty"`
}

// NotificationRule maps S3 events to notification targets.
type NotificationRule struct {
	ID        string   `json:"Id"`
	Events    []string `json:"Events"`
	FilterKey string   `json:"FilterKey,omitempty"`
	QueueARN  string   `json:"QueueArn,omitempty"`
	TopicARN  string   `json:"TopicArn,omitempty"`
	LambdaARN string   `json:"LambdaFunctionArn"`
}

// CORSRule defines one CORS rule for a bucket.
type CORSRule struct {
	AllowedOrigins []string `json:"AllowedOrigins"`
	AllowedMethods []string `json:"AllowedMethods"`
	AllowedHeaders []string `json:"AllowedHeaders"`
	ExposeHeaders  []string `json:"ExposeHeaders"`
	MaxAgeSeconds  int      `json:"MaxAgeSeconds"`
}

// LoggingConfig holds the server-access-logging target for a bucket.
type LoggingConfig struct {
	Enabled bool
	Target  string
	Prefix  string
}

// BucketStats holds aggregate storage statistics for a single bucket.
type BucketStats struct {
	Bucket      string `json:"bucket"`
	ObjectCount int64  `json:"object_count"`
	TotalSize   int64  `json:"total_size_bytes"`
}

// ListPage is a paginated slice of Object rows.
type ListPage struct {
	Objects    []Object
	NextMarker string
	HasMore    bool
}

// VersionListOpts controls paginated version listing.
type VersionListOpts struct {
	VersionIDMarker string
	Limit           int
}

// VersionListPage is a paginated slice of Object versions.
type VersionListPage struct {
	Versions      []Object
	NextVersionID string
	HasMore       bool
}

// Upload tracks an in-progress multipart upload.
type Upload struct {
	ID             string
	TenantID       string
	Bucket         string
	Key            string
	StorageKey     string
	UploadID       string
	ContentType    string
	Metadata       map[string]string
	StorageClass   string
	Backend        string
	BackendUID     string
	TotalParts     int
	CompletedParts int
	CreatedAt      time.Time
}

// PartRecord tracks a single uploaded part.
type PartRecord struct {
	ID         int64
	UploadID   string
	PartNumber int32
	Size       int64
	ETag       string
	CreatedAt  time.Time
}

// Event lifecycle event.
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

// EventType categorises an Event.
type EventType string

const (
	EventCreated  EventType = "created"
	EventUpdated  EventType = "updated"
	EventDeleted  EventType = "deleted"
	EventAccessed EventType = "accessed"
)

// Chunk holds an embedded text chunk with its vector.
type Chunk struct {
	ID         int64
	ObjectID   int64
	TenantID   string
	Bucket     string
	ObjectKey  string
	Seq        int
	Content    string
	Model      string
	Embedding  []float32
	Dim        int
	EmbedModel string
	CreatedAt  time.Time
}

// Usage records one AI consumption event.
type Usage struct {
	ID               int64
	TenantID         string
	Caller           string
	Query            string
	ChunkIDs         []int64
	ObjectIDs        []int64
	RequestID        string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMs        int64
	CostMicros       int64
	CreatedAt        time.Time
}

// SearchHit is a ranked result from vector/hybrid search.
type SearchHit struct {
	Chunk      Chunk
	Score      float32
	Rank       int
	ObjectID   int64
	ObjectKey  string
	Bucket     string
	EmbedModel string
}

// IdempotencyRecord tracks a claimed Idempotency-Key.
type IdempotencyRecord struct {
	TenantID        string
	Key             string
	Fingerprint     string
	Status          string
	RequestID       string
	ResponseStatus  int
	ResponseBody    []byte
	ResponseCT      string
	ResponseHeaders map[string][]string
	CompletedAt     string
	CreatedAt       string
}

// APIKeyRecord is the persisted form of an API key.
type APIKeyRecord struct {
	TokenHash  string
	TenantID   string
	Scopes     string
	Label      string
	CreatedAt  string
	ExpiresAt  string
	LastUsedAt string
}

// TenantRecord holds runtime tenant metadata.
type TenantRecord struct {
	TenantID          string `json:"tenant_id"`
	DisplayName       string `json:"display_name"`
	Status            string `json:"status"`
	DailyBudgetMicros int64  `json:"daily_budget_micros"`
	StorageUsedBytes  int64  `json:"storage_used_bytes"`
	ObjectCount       int64  `json:"object_count"`
	CreatedAt         string `json:"created_at"`
}

// LegalHold records a compliance hold on an object.
type LegalHold struct {
	ID         int64  `json:"id"`
	ObjectID   int64  `json:"object_id"`
	VersionID  string `json:"version_id"`
	TenantID   string `json:"tenant_id"`
	Bucket     string `json:"bucket"`
	ObjectKey  string `json:"object_key"`
	HoldReason string `json:"hold_reason"`
	CreatedAt  string `json:"created_at"`
	CreatedBy  string `json:"created_by"`
}

// AuditEntry records an admin action.
type AuditEntry struct {
	ID        int64  `json:"id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	TenantID  string `json:"tenant_id"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"created_at"`
}

const defaultStorageClass = "STANDARD"
