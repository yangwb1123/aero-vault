package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"

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
	ErrNotFound             = errors.New("object not found")
	ErrInvalidArgs          = errors.New("invalid arguments")
	ErrUploadNotFound       = errors.New("upload not found")
	ErrLocked               = errors.New("object is under retention lock")
	ErrQuotaExceeded        = errors.New("tenant quota exceeded")
	ErrRangeNotSatisfiable  = errors.New("requested range not satisfiable")
	ErrPreconditionFailed   = errors.New("precondition failed")
	ErrForbidden            = errors.New("forbidden")
	ErrBadDigest            = errors.New("content-md5 mismatch")
	ErrSizeMismatch         = errors.New("size mismatch: actual bytes differ from Content-Length")
	ErrObjectCorrupt        = errors.New("object is marked as corrupt")
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
	store        storage.Storage
	repo         repository.Repository
	logger       *slog.Logger
	sink         EventSink
	chunkCleaner ChunkCleaner
	readVerify   ReadVerificationConfig
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

// validateMetadata checks size and key-length constraints on user metadata.
// System keys (prefixed _aero_) are exempt. Returns nil when meta is empty.
func validateMetadata(meta map[string]string) error {
	if len(meta) == 0 {
		return nil
	}
	var total int
	for k, v := range meta {
		if strings.HasPrefix(k, "_aero_") {
			continue
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
	ContentType  string
	Metadata     map[string]string
	Tags         map[string]string
	ContentMD5   string
	StorageClass string
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
