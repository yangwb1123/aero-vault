package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"reflect"
	"strings"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// DefaultBucket is used by REST callers that don't pass a bucket.
const DefaultBucket = "default"

// DefaultStorageClass is the default S3 storage class.
var DefaultStorageClass = "STANDARD"

// DefaultTenant is used when no X-Aero-Tenant header is set.
const DefaultTenant = "default"

var (
	ErrNotFound               = errors.New("object not found")
	ErrInvalidArgs            = errors.New("invalid arguments")
	ErrUploadNotFound         = errors.New("upload not found")
	ErrLocked                 = errors.New("object is under retention lock")
	ErrQuotaExceeded          = errors.New("tenant quota exceeded")
	ErrEntitlementUnavailable = errors.New("tenant entitlement unavailable")
	// ErrTimeout is the server-side request deadline sentinel (thumbnail
	// derivation path and any future bounded pipeline): the client may still
	// be connected, so it surfaces as a visible 504 Gateway Timeout rather
	// than a silent empty response.
	ErrTimeout             = errors.New("request timed out")
	ErrRangeNotSatisfiable = errors.New("requested range not satisfiable")
	ErrPreconditionFailed  = errors.New("precondition failed")
	ErrForbidden           = errors.New("forbidden")
	ErrTenantDisabled      = errors.New("tenant is disabled")
	ErrBadDigest           = errors.New("content-md5 mismatch")
	ErrSizeMismatch        = errors.New("size mismatch: actual bytes differ from Content-Length")
	ErrObjectCorrupt       = errors.New("object is marked as corrupt")
	// ErrMetadataTooLarge is the object-metadata size limit (total bytes
	// across all user metadata k/v). Do not confuse it with
	// thumbnail.ErrMetadataTooLarge, the unrelated image-metadata budget for
	// thumbnail decoding (internal/thumbnail/thumbnail.go).
	ErrMetadataTooLarge     = errors.New("metadata too large: total exceeds 64 KiB")
	ErrMetadataKeyTooLong   = errors.New("metadata key too long: max 256 bytes")
	ErrMetadataValueTooLong = errors.New("metadata value too long: max 64 KiB")

	// MaxMetadataSize is the maximum total bytes across all user metadata k/v.
	MaxMetadataSize = 64 * 1024 // 64 KiB
	// MaxMetadataKeyLen is the maximum length of a single metadata key.
	MaxMetadataKeyLen = 256
	// MaxMetadataValueLen is the maximum length of a single metadata value.
	MaxMetadataValueLen = 64 * 1024 // 64 KiB
)

// EventSink receives lifecycle events produced by the FileService. The
// implementation in /internal/events persists them to the repository and wakes
// any subscribers.
type EventSink interface {
	Publish(ctx context.Context, e repository.Event)
}

// ChunkCleaner is an optional hook called synchronously on hard delete, before
// the repository row is removed, to clean up any secondary chunk index entries
// (BM25, vector store, etc.). Non-fatal: a failure is logged but the hard
// delete proceeds.
type ChunkCleaner interface {
	DeleteObjectChunks(ctx context.Context, objectID int64) error
}

// ShareLister is the read-only capability lookup used to enrich privileged
// delete facts. It is deliberately narrower than access.Store so wrappers and
// test doubles can provide only the required repository operation.
type ShareLister interface {
	ListShares(ctx context.Context, tenant, bucket, key string) ([]access.Share, error)
}

type noopSink struct{}

func (noopSink) Publish(context.Context, repository.Event) {}

// FileService is the single entry point shared by REST + S3-compat handlers.
// ReadVerificationConfig controls optional on-read integrity verification.
type ReadVerificationConfig struct {
	// Enabled, when true, wraps every Get reader with an ETagVerifier that
	// recomputes the content hash and compares it against the stored ETag.
	Enabled bool
	// MaxSize is the maximum object size (in bytes) for full-content verification.
	// Objects larger than this threshold use sampling instead.
	MaxSize int64
	// Sample enables byte-range sampling for objects exceeding MaxSize.
	Sample bool
}

type FileService struct {
	store           storage.Storage
	repo            repository.Repository
	logger          *slog.Logger
	sink            EventSink
	chunkCleaner    ChunkCleaner
	shareLister     ShareLister
	readVerify      ReadVerificationConfig
	authorizer      access.Authorizer
	deleteFailOpen  bool
	tenantStatus    bool
	usageAccountant UsageAccountant
}

// WithAuthorizer enables resource-level authorization at the FileService
// boundary. A nil authorizer preserves non-delete compatibility paths; object
// deletion remains fail-closed unless explicitly opted out.
func (s *FileService) WithAuthorizer(authorizer access.Authorizer) *FileService {
	// nil 防御：接口持有的 (*Manager)(nil) 会让 `authorizer != nil` 误判为
	// true，随后 authorize() 调用 nil 方法 panic（真实事故：PUT /v1/files 500，
	// access 禁用时 minimal 部署必现）。两层防御：具体类型 nil + 反射兜底。
	if authorizer == nil {
		return s
	}
	value := reflect.ValueOf(authorizer)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		if value.IsNil() {
			return s
		}
	}
	s.authorizer = authorizer
	return s
}

// WithDeleteFailOpen opts out of the fail-closed delete gate (FR-1): when
// optOut is true and no authorizer is configured, deletes are allowed again
// (legacy baseline). The zero value (not called) is fail-closed: ActionDelete
// with a nil authorizer returns ErrForbidden. Trusted system contexts remain
// exempt for internal maintenance paths (access.IsSystemDeleteExempt).
func (s *FileService) WithDeleteFailOpen(optOut bool) *FileService {
	s.deleteFailOpen = optOut
	return s
}

// WithTenantStatusEnforcement rejects data-plane operations for known disabled
// tenants. Unknown tenants preserve the implicit-tenant compatibility model.
func (s *FileService) WithTenantStatusEnforcement() *FileService {
	s.tenantStatus = true
	return s
}

// WithUsageAccountant attaches subscription-aware quota checks and durable
// usage accounting. The accountant must keep local usage and its outbox in one
// transaction; it must never make FileService request success depend on a
// synchronous remote call.
func (s *FileService) WithUsageAccountant(accountant UsageAccountant) *FileService {
	s.usageAccountant = accountant
	return s
}

func NewFileService(store storage.Storage, repo repository.Repository, logger *slog.Logger) *FileService {
	if logger == nil {
		logger = slog.Default()
	}
	return &FileService{store: store, repo: repo, logger: logger, sink: noopSink{}}
}

// WithEventSink attaches a publisher. Returns the service for fluent wiring.
func (s *FileService) WithEventSink(sink EventSink) *FileService {
	if sink == nil {
		s.sink = noopSink{}
	} else {
		s.sink = sink
	}
	return s
}

// WithChunkCleaner attaches a synchronous chunk-cleanup hook for hard deletes.
func (s *FileService) WithChunkCleaner(cc ChunkCleaner) *FileService {
	if cc == nil {
		s.chunkCleaner = nil
	} else {
		s.chunkCleaner = cc
	}
	return s
}

// WithShareLister attaches the optional capability reader used by AdminDelete
// to capture active share IDs before the transactional cascade.
func (s *FileService) WithShareLister(lister ShareLister) *FileService {
	s.shareLister = lister
	return s
}

// ChunkCleaner returns the attached chunk cleaner, or nil. Used by reconcile
// workers to clean up AI chunks when purging soft-deleted objects.
func (s *FileService) ChunkCleaner() ChunkCleaner {
	return s.chunkCleaner
}

// WithReadVerification enables optional on-read ETag integrity verification.
func (s *FileService) WithReadVerification(cfg ReadVerificationConfig) *FileService {
	s.readVerify = cfg
	return s
}

// validateMetadata checks user metadata. The _aero_ namespace is reserved for
// service-owned integrity, encryption, retention, and multipart state.
func validateMetadata(meta map[string]string) error {
	if len(meta) == 0 {
		return nil
	}
	var total int
	for k, v := range meta {
		if strings.HasPrefix(strings.ToLower(k), "_aero_") {
			return fmt.Errorf("%w: metadata key %q uses reserved _aero_ namespace", ErrInvalidArgs, k)
		}
		if len(k) > MaxMetadataKeyLen {
			return fmt.Errorf("%w: %q (%d bytes)", ErrMetadataKeyTooLong, k, len(k))
		}
		if len(v) > MaxMetadataValueLen {
			return fmt.Errorf("%w: key %q: %d bytes > %d",
				ErrMetadataValueTooLong, k, len(v), MaxMetadataValueLen)
		}
		total += len(k) + len(v)
		if total > MaxMetadataSize {
			return fmt.Errorf("%w: %d bytes > %d", ErrMetadataTooLarge, total, MaxMetadataSize)
		}
	}
	return nil
}

// MaxKeyLen is the maximum length of an object key. Keys longer than this
// are rejected to prevent filesystem "file name too long" errors on local
// storage backends (where the key becomes part of the file path).
const MaxKeyLen = 200

// validateKey rejects empty keys, absolute paths, path-traversal keys, and
// keys exceeding MaxKeyLen.
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty key", ErrInvalidArgs)
	}
	if len(key) > MaxKeyLen {
		return fmt.Errorf("%w: key length %d exceeds maximum %d", ErrInvalidArgs, len(key), MaxKeyLen)
	}
	if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: illegal key %q", ErrInvalidArgs, key)
	}
	return nil
}

// Repo exposes the underlying repository for callers that need raw access.
func (s *FileService) Repo() repository.Repository { return s.repo }

// PutOptions mirrors storage.PutOptions plus tags and optional Content-MD5.
type PutOptions struct {
	ContentType        string
	ContentDisposition string
	ContentEncoding    string
	Metadata           map[string]string
	Tags               map[string]string
	ACL                string
	LegalHold          bool
	ContentMD5         string
	StorageClass       string
	SSEAlgorithm       string
	SSEKMSKeyID        string
	SSECustomerKey     []byte
	SSECustomerKeyMD5  []byte
}

// ReadOptions carries request-scoped encryption information.
type ReadOptions struct {
	SSECustomerKey    []byte
	SSECustomerKeyMD5 []byte
}

// WithDefaultStorageClass overrides package-level DefaultStorageClass.
func WithDefaultStorageClass(sc string) {
	if sc != "" {
		DefaultStorageClass = sc
	}
}

// callerFrom extracts a caller identity from the context, preferring the API key
// label when available and falling back to the tenant ID.
func callerFrom(ctx context.Context) string {
	t := middleware.TenantFrom(ctx)
	// If auth middleware stashed a key label, use that.
	if label, ok := ctx.Value("auth_key_label").(string); ok && label != "" {
		return label
	}
	return t
}

// StorageClassOrDefault returns StorageClass or DefaultStorageClass.
func StorageClassOrDefault(sc string) string {
	if sc == "" {
		return DefaultStorageClass
	}
	return sc
}

func storageKey(tenant, bucket, key string) string {
	if !strings.HasPrefix(key, "/") {
		return path.Join(tenant, bucket, key)
	}
	// key may start with "/" from S3 path-style requests.
	return path.Join(tenant, bucket, strings.TrimPrefix(key, "/"))
}

func defaults(tenant, bucket string) (string, string) {
	if tenant == "" {
		tenant = DefaultTenant
	}
	if bucket == "" {
		bucket = DefaultBucket
	}
	return tenant, bucket
}

func checkedObjectDefaults(tenant, bucket, key string) (string, string, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return "", "", err
	}
	return tenant, bucket, nil
}

// uploadStorageKey returns the upload's persisted assembly key, falling back to
// the recomputed unversioned key for uploads created before storage_key was
// tracked (in-flight across the migration).
func uploadStorageKey(u repository.Upload) string {
	if u.StorageKey != "" {
		return u.StorageKey
	}
	return storageKey(u.TenantID, u.Bucket, u.Key)
}

func (s *FileService) Storage() storage.Storage { return s.store }

// EmitAccessed publishes an EventAccessed for obj without opening any stream
// or holding a decode slot. The REST thumbnail handler calls it on the
// server-cache hit path so every successful 200 thumbnail response emits
// exactly one access event (misses already emit via the stream-open path in
// openObjectWithOptions). Best-effort like every other event emission.
func (s *FileService) EmitAccessed(ctx context.Context, obj repository.Object) {
	s.emit(ctx, obj, repository.EventAccessed)
}

// emit constructs a minimal Event payload and hands it to the sink. We swallow
// errors from the sink: lifecycle events are best-effort and must never break a
// user request.
func (s *FileService) emit(ctx context.Context, o repository.Object, t repository.EventType) {
	id := o.ID
	e := repository.Event{
		TenantID:  o.TenantID,
		Bucket:    o.Bucket,
		Key:       o.Key,
		Type:      t,
		ObjectID:  &id,
		RequestID: middleware.RequestIDFrom(ctx),
		Payload: map[string]string{
			"backend":      o.Backend,
			"size":         fmt.Sprintf("%d", o.Size),
			"etag":         o.ETag,
			"content_type": o.ContentType,
		},
	}
	s.sink.Publish(ctx, e)
}
