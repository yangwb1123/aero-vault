package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// DefaultBucket is used by REST callers that don't pass a bucket.
const DefaultBucket = "default"

// DefaultTenant is used when no X-Aero-Tenant header is set.
const DefaultTenant = "default"

var (
	ErrNotFound            = errors.New("object not found")
	ErrInvalidArgs         = errors.New("invalid arguments")
	ErrUploadNotFound      = errors.New("upload not found")
	ErrLocked              = errors.New("object is under retention lock")
	ErrQuotaExceeded       = errors.New("tenant quota exceeded")
	ErrRangeNotSatisfiable = errors.New("requested range not satisfiable")
	ErrPreconditionFailed  = errors.New("precondition failed")
	ErrForbidden           = errors.New("forbidden")
)

// EventSink receives lifecycle events produced by the FileService. The
// implementation in /internal/events persists them to the repository and wakes
// any subscribers.
type EventSink interface {
	Publish(ctx context.Context, e repository.Event)
}

type noopSink struct{}

func (noopSink) Publish(context.Context, repository.Event) {}

// FileService is the single entry point shared by REST + S3-compat handlers.
type FileService struct {
	store  storage.Storage
	repo   repository.Repository
	logger *slog.Logger
	sink   EventSink
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

// Repo exposes the underlying repository for callers that need raw access
// (search/audit endpoints). Do not bypass it for object writes.
func (s *FileService) Repo() repository.Repository { return s.repo }

// PutOptions mirrors storage.PutOptions plus tags.
type PutOptions struct {
	ContentType string
	Metadata    map[string]string
	Tags        map[string]string
}

func storageKey(tenant, bucket, key string) string {
	return path.Join(tenant, bucket, key)
}

func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty key", ErrInvalidArgs)
	}
	if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: illegal key %q", ErrInvalidArgs, key)
	}
	return nil
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

// Put streams r into storage and records metadata. When the bucket has
// versioning enabled, every Put creates a new historical row; otherwise the
// existing row is upserted in place.
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, r io.Reader, size int64, opts PutOptions) (repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return repository.Object{}, err
	}
	if err := s.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		return repository.Object{}, fmt.Errorf("ensure bucket: %w", err)
	}
	bcfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return repository.Object{}, fmt.Errorf("get bucket config: %w", err)
	}
	// Quota enforcement up front so we don't waste storage bandwidth.
	if size > 0 {
		q, qErr := s.repo.GetTenantQuota(ctx, tenant)
		if qErr == nil {
			if q.MaxBytes > 0 && q.UsedBytes+size > q.MaxBytes {
				return repository.Object{}, fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, q.UsedBytes+size, q.MaxBytes)
			}
			if q.MaxObjects > 0 && q.UsedObjects+1 > q.MaxObjects {
				return repository.Object{}, fmt.Errorf("%w: objects %d/%d", ErrQuotaExceeded, q.UsedObjects+1, q.MaxObjects)
			}
		}
	} else {
		// Size unknown (e.g. a chunked/streaming PUT with no Content-Length): the
		// delta can't be pre-checked, but still refuse when the tenant is already at
		// its byte or object cap so an unsized upload can't bypass quota entirely.
		q, qErr := s.repo.GetTenantQuota(ctx, tenant)
		if qErr == nil {
			if q.MaxBytes > 0 && q.UsedBytes >= q.MaxBytes {
				return repository.Object{}, fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, q.UsedBytes, q.MaxBytes)
			}
			if q.MaxObjects > 0 && q.UsedObjects >= q.MaxObjects {
				return repository.Object{}, fmt.Errorf("%w: objects %d/%d", ErrQuotaExceeded, q.UsedObjects, q.MaxObjects)
			}
		}
	}
	// Check existing object lock before overwriting.
	if !bcfg.Versioning {
		if cur, err := s.repo.GetObject(ctx, tenant, bucket, key); err == nil {
			if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
				return repository.Object{}, fmt.Errorf("%w: overwrite blocked until %s", ErrLocked, cur.LockedUntil.Format(time.RFC3339))
			}
		}
	}

	// For versioning, use a unique storage_key per version so old content is preserved.
	sk := storageKey(tenant, bucket, key)
	if bcfg.Versioning {
		sk = sk + "@v" + repoVersionID() // placeholder; will be replaced after repo assigns the real version_id
	}
	info, err := s.store.Put(ctx, sk, r, size, storage.PutOptions{
		ContentType: opts.ContentType,
		Metadata:    opts.Metadata,
	})
	if err != nil {
		return repository.Object{}, fmt.Errorf("storage put: %w", err)
	}
	obj := repository.Object{
		TenantID:    tenant,
		Bucket:      bucket,
		Key:         key,
		Backend:     s.store.Backend(),
		StorageKey:  sk,
		Size:        info.Size,
		ETag:        info.ETag,
		ContentType: info.ContentType,
		Metadata:    opts.Metadata,
		Tags:        opts.Tags,
	}
	// Apply object-lock retention defaults from the bucket config.
	if bcfg.ObjectLockSeconds > 0 {
		until := time.Now().Add(time.Duration(bcfg.ObjectLockSeconds) * time.Second)
		obj.LockedUntil = &until
	}

	var saved repository.Object
	if bcfg.Versioning {
		saved, err = s.repo.InsertObjectVersion(ctx, obj)
	} else {
		saved, err = s.repo.UpsertObject(ctx, obj)
	}
	if err != nil {
		s.logger.Error("repo write failed; storage object orphaned", "tenant", tenant, "bucket", bucket, "key", key, "err", err)
		return repository.Object{}, fmt.Errorf("repo write: %w", err)
	}
	if bcfg.ObjectLockSeconds > 0 && saved.LockedUntil == nil {
		until := time.Now().Add(time.Duration(bcfg.ObjectLockSeconds) * time.Second)
		_ = s.repo.SetLockedUntil(ctx, tenant, bucket, key, until)
		saved.LockedUntil = &until
	}
	// Account against the tenant quota (best effort).
	if _, qErr := s.repo.AddTenantUsage(ctx, tenant, saved.Size, 1); qErr != nil {
		s.logger.Warn("quota usage increment failed", "tenant", tenant, "err", qErr)
	}
	s.emit(ctx, saved, repository.EventCreated)
	return saved, nil
}

// repoVersionID is a process-local helper that produces a short suffix for
// per-version storage keys. The authoritative version_id lives in the
// repository; this is only used to keep backend keys unique.
func repoVersionID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// Get streams an object from storage; caller closes the reader.
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, repository.Object{}, ErrNotFound
		}
		return nil, repository.Object{}, err
	}
	rc, _, err := s.store.Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, repository.Object{}, ErrNotFound
		}
		return nil, repository.Object{}, err
	}
	s.emit(ctx, obj, repository.EventAccessed)
	return rc, obj, nil
}

// Stat returns metadata only.
func (s *FileService) Stat(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return repository.Object{}, ErrNotFound
		}
		return repository.Object{}, err
	}
	return obj, nil
}

// Delete removes an object. When hard is true the storage object is also
// removed. Hard delete fails for objects under retention lock.
func (s *FileService) Delete(ctx context.Context, tenant, bucket, key string, hard bool) error {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if hard {
		if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
			return fmt.Errorf("%w: hard delete blocked until %s", ErrLocked, obj.LockedUntil.Format(time.RFC3339))
		}
		if err := s.store.Delete(ctx, obj.StorageKey); err != nil {
			return fmt.Errorf("storage delete: %w", err)
		}
		if err := s.repo.HardDeleteObject(ctx, tenant, bucket, key); err != nil {
			return err
		}
		if _, qErr := s.repo.AddTenantUsage(ctx, tenant, -obj.Size, -1); qErr != nil {
			s.logger.Warn("quota decrement on hard delete failed", "err", qErr)
		}
		s.emit(ctx, obj, repository.EventDeleted)
		return nil
	}
	if err := s.repo.SoftDeleteObject(ctx, tenant, bucket, key); err != nil {
		return err
	}
	if _, qErr := s.repo.AddTenantUsage(ctx, tenant, -obj.Size, -1); qErr != nil {
		s.logger.Warn("quota decrement on soft delete failed", "err", qErr)
	}
	s.emit(ctx, obj, repository.EventDeleted)
	return nil
}

// Usage returns the tenant's current usage + caps.
func (s *FileService) Usage(ctx context.Context, tenant string) (repository.TenantQuota, error) {
	tenant, _ = defaults(tenant, "")
	return s.repo.GetTenantQuota(ctx, tenant)
}

// SetQuota updates the caps for a tenant.
func (s *FileService) SetQuota(ctx context.Context, tenant string, maxBytes, maxObjects int64) error {
	tenant, _ = defaults(tenant, "")
	return s.repo.SetTenantQuota(ctx, tenant, maxBytes, maxObjects)
}

// SetTags overwrites the tag set on an object.
func (s *FileService) SetTags(ctx context.Context, tenant, bucket, key string, tags map[string]string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.UpdateTags(ctx, tenant, bucket, key, tags)
}

// ListVersions returns every version of a key, newest first.
func (s *FileService) ListVersions(ctx context.Context, tenant, bucket, key string) ([]repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.ListObjectVersions(ctx, tenant, bucket, key)
}

// GetVersion fetches the content for a specific version_id.
func (s *FileService) GetVersion(ctx context.Context, tenant, bucket, key, versionID string) (io.ReadCloser, repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObjectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, repository.Object{}, ErrNotFound
		}
		return nil, repository.Object{}, err
	}
	rc, _, err := s.store.Get(ctx, obj.StorageKey)
	if err != nil {
		return nil, repository.Object{}, err
	}
	return rc, obj, nil
}

// LockObject sets a retention deadline on a specific object.
func (s *FileService) LockObject(ctx context.Context, tenant, bucket, key string, until time.Time) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetLockedUntil(ctx, tenant, bucket, key, until)
}

// SetBucketVersioning toggles versioning for the given bucket.
func (s *FileService) SetBucketVersioning(ctx context.Context, tenant, bucket string, enabled bool) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketVersioning(ctx, tenant, bucket, enabled)
}

// SetBucketObjectLock configures default retention for the given bucket.
func (s *FileService) SetBucketObjectLock(ctx context.Context, tenant, bucket string, seconds int) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketObjectLock(ctx, tenant, bucket, seconds)
}

// SetBucketLifecycle configures auto-expiry: ExpireAfterDays from updated_at,
// action is "soft_delete" (default) or "hard_delete".
func (s *FileService) SetBucketLifecycle(ctx context.Context, tenant, bucket string, days int, action string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketLifecycle(ctx, tenant, bucket, days, action)
}

// GetBucketConfig returns the per-bucket policy.
func (s *FileService) GetBucketConfig(ctx context.Context, tenant, bucket string) (repository.BucketConfig, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.GetBucketConfig(ctx, tenant, bucket)
}

// List paginates objects sharing a prefix.
func (s *FileService) List(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (repository.ListPage, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.ListObjects(ctx, tenant, bucket, prefix, marker, limit)
}

// PresignGet returns a time-limited download URL.
func (s *FileService) PresignGet(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	return s.store.PresignGet(ctx, obj.StorageKey, expiry)
}

// PresignPut returns a time-limited upload URL.
func (s *FileService) PresignPut(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return "", err
	}
	return s.store.PresignPut(ctx, storageKey(tenant, bucket, key), expiry)
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

// InitMultipart opens a multipart upload and persists the session.
func (s *FileService) InitMultipart(ctx context.Context, tenant, bucket, key string, opts PutOptions) (repository.Upload, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return repository.Upload{}, err
	}
	if err := s.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		return repository.Upload{}, err
	}
	bcfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return repository.Upload{}, fmt.Errorf("get bucket config: %w", err)
	}
	// On a versioned bucket, assemble into a unique per-version storage key so the
	// completed object becomes a new version instead of overwriting the current one
	// (mirrors Put's one-blob-per-version scheme).
	sk := storageKey(tenant, bucket, key)
	if bcfg.Versioning {
		sk = sk + "@v" + repoVersionID()
	}
	init, err := s.store.InitMultipart(ctx, sk, storage.PutOptions{
		ContentType: opts.ContentType,
		Metadata:    opts.Metadata,
	})
	if err != nil {
		return repository.Upload{}, fmt.Errorf("storage init multipart: %w", err)
	}
	u := repository.Upload{
		ID:         init.UploadID,
		TenantID:   tenant,
		Bucket:     bucket,
		Key:        key,
		Backend:    s.store.Backend(),
		BackendUID: init.UploadID,
		StorageKey: sk,
		Metadata:   opts.Metadata,
	}
	if err := s.repo.CreateUpload(ctx, u); err != nil {
		_ = s.store.AbortMultipart(ctx, sk, init.UploadID)
		return repository.Upload{}, fmt.Errorf("repo create upload: %w", err)
	}
	return u, nil
}

func (s *FileService) UploadPart(ctx context.Context, uploadID string, partNumber int32, r io.Reader, size int64) (repository.PartRecord, error) {
	u, err := s.repo.GetUpload(ctx, uploadID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			return repository.PartRecord{}, ErrUploadNotFound
		}
		return repository.PartRecord{}, err
	}
	sk := uploadStorageKey(u)
	part, err := s.store.UploadPart(ctx, sk, u.BackendUID, partNumber, r, size)
	if err != nil {
		return repository.PartRecord{}, fmt.Errorf("storage upload part: %w", err)
	}
	rec := repository.PartRecord{
		UploadID:   uploadID,
		PartNumber: partNumber,
		ETag:       part.ETag,
		Size:       size,
	}
	if err := s.repo.RecordPart(ctx, rec); err != nil {
		return repository.PartRecord{}, fmt.Errorf("repo record part: %w", err)
	}
	return rec, nil
}

func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
	u, err := s.repo.GetUpload(ctx, uploadID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			return repository.Object{}, ErrUploadNotFound
		}
		return repository.Object{}, err
	}
	parts, err := s.repo.ListParts(ctx, uploadID)
	if err != nil {
		return repository.Object{}, err
	}
	if len(parts) == 0 {
		return repository.Object{}, fmt.Errorf("%w: no parts uploaded", ErrInvalidArgs)
	}
	storageParts := make([]storage.MultipartPart, 0, len(parts))
	var total int64
	for _, p := range parts {
		storageParts = append(storageParts, storage.MultipartPart{PartNumber: p.PartNumber, ETag: p.ETag})
		total += p.Size
	}

	bcfg, err := s.repo.GetBucketConfig(ctx, u.TenantID, u.Bucket)
	if err != nil {
		return repository.Object{}, fmt.Errorf("get bucket config: %w", err)
	}
	// Quota enforcement before assembling — multipart must respect caps like a
	// single PUT does (the total part size is known here).
	if q, qErr := s.repo.GetTenantQuota(ctx, u.TenantID); qErr == nil {
		if q.MaxBytes > 0 && q.UsedBytes+total > q.MaxBytes {
			return repository.Object{}, fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, q.UsedBytes+total, q.MaxBytes)
		}
		if q.MaxObjects > 0 && q.UsedObjects+1 > q.MaxObjects {
			return repository.Object{}, fmt.Errorf("%w: objects %d/%d", ErrQuotaExceeded, q.UsedObjects+1, q.MaxObjects)
		}
	}
	// Respect object-lock on overwrite (non-versioned buckets), like Put.
	if !bcfg.Versioning {
		if cur, gErr := s.repo.GetObject(ctx, u.TenantID, u.Bucket, u.Key); gErr == nil {
			if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
				return repository.Object{}, fmt.Errorf("%w: overwrite blocked until %s", ErrLocked, cur.LockedUntil.Format(time.RFC3339))
			}
		}
	}

	sk := uploadStorageKey(u)
	info, err := s.store.CompleteMultipart(ctx, sk, u.BackendUID, storageParts)
	if err != nil {
		return repository.Object{}, fmt.Errorf("storage complete: %w", err)
	}
	if info.Size == 0 {
		info.Size = total
	}

	obj := repository.Object{
		TenantID:    u.TenantID,
		Bucket:      u.Bucket,
		Key:         u.Key,
		Backend:     s.store.Backend(),
		StorageKey:  sk,
		Size:        info.Size,
		ETag:        info.ETag,
		ContentType: info.ContentType,
		Metadata:    u.Metadata,
	}
	if bcfg.ObjectLockSeconds > 0 {
		until := time.Now().Add(time.Duration(bcfg.ObjectLockSeconds) * time.Second)
		obj.LockedUntil = &until
	}
	var saved repository.Object
	if bcfg.Versioning {
		// New version (sk is the unique @v key from InitMultipart).
		saved, err = s.repo.InsertObjectVersion(ctx, obj)
	} else {
		saved, err = s.repo.UpsertObject(ctx, obj)
	}
	if err != nil {
		s.logger.Error("repo write after multipart failed; orphan", "key", u.Key, "err", err)
		return repository.Object{}, fmt.Errorf("repo write: %w", err)
	}
	if bcfg.ObjectLockSeconds > 0 && saved.LockedUntil == nil {
		until := time.Now().Add(time.Duration(bcfg.ObjectLockSeconds) * time.Second)
		_ = s.repo.SetLockedUntil(ctx, u.TenantID, u.Bucket, u.Key, until)
		saved.LockedUntil = &until
	}
	// Account the upload against the tenant quota (best effort) — previously
	// multipart uploads consumed storage without ever incrementing usage.
	if _, qErr := s.repo.AddTenantUsage(ctx, u.TenantID, saved.Size, 1); qErr != nil {
		s.logger.Warn("quota usage increment failed", "tenant", u.TenantID, "err", qErr)
	}
	_ = s.repo.DeleteUpload(ctx, uploadID)
	s.emit(ctx, saved, repository.EventCreated)
	return saved, nil
}

func (s *FileService) AbortMultipart(ctx context.Context, uploadID string) error {
	u, err := s.repo.GetUpload(ctx, uploadID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			return ErrUploadNotFound
		}
		return err
	}
	sk := uploadStorageKey(u)
	_ = s.store.AbortMultipart(ctx, sk, u.BackendUID)
	return s.repo.DeleteUpload(ctx, uploadID)
}

func (s *FileService) HeadBucket(ctx context.Context, tenant, bucket string) (bool, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.BucketExists(ctx, tenant, bucket)
}

func (s *FileService) CreateBucket(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.CreateBucket(ctx, tenant, bucket)
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
