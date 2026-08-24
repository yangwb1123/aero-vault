package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var tracer = telemetry.Tracer("aero-vault/service")

// preflightQuota checks tenant quota before a write. Repository failures are
// returned so a database outage cannot silently bypass the configured cap and
// leave a newly written blob without consistent accounting.
func (s *FileService) preflightQuota(ctx context.Context, tenant string, deltaBytes, deltaObjects int64) error {
	q, qErr := s.repo.GetTenantQuota(ctx, tenant)
	if qErr != nil {
		return fmt.Errorf("get tenant quota: %w", qErr)
	}
	if s.usageAccountant != nil {
		if err := s.usageAccountant.CheckQuota(ctx, tenant, q, deltaBytes, deltaObjects); err != nil {
			return err
		}
	}
	if err := checkBytesQuota(q, deltaBytes); err != nil {
		return err
	}
	return checkObjectsQuota(q, deltaObjects)
}

// preflightBucketQuota checks per-bucket storage limits before a write.
func (s *FileService) preflightBucketQuota(ctx context.Context, tenant, bucket string, deltaBytes, deltaObjects int64) error {
	bcfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return err
	}
	if bcfg.BucketMaxBytes <= 0 && bcfg.BucketMaxObjects <= 0 {
		return nil // unlimited
	}
	usedBytes, usedObjects, err := s.repo.BucketUsage(ctx, tenant, bucket)
	if err != nil {
		return err
	}
	if deltaBytes > 0 && bcfg.BucketMaxBytes > 0 && usedBytes+deltaBytes > bcfg.BucketMaxBytes {
		return fmt.Errorf("%w: bucket %s bytes %d/%d", ErrQuotaExceeded, bucket, usedBytes+deltaBytes, bcfg.BucketMaxBytes)
	}
	if deltaObjects > 0 && bcfg.BucketMaxObjects > 0 && usedObjects+deltaObjects > bcfg.BucketMaxObjects {
		return fmt.Errorf("%w: bucket %s objects %d/%d", ErrQuotaExceeded, bucket, usedObjects+deltaObjects, bcfg.BucketMaxObjects)
	}
	return nil
}

func checkBytesQuota(q repository.TenantQuota, delta int64) error {
	if delta > 0 && q.MaxBytes > 0 && q.UsedBytes+delta > q.MaxBytes {
		return fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, q.UsedBytes+delta, q.MaxBytes)
	}
	return nil
}

func checkObjectsQuota(q repository.TenantQuota, delta int64) error {
	if delta > 0 && q.MaxObjects > 0 && q.UsedObjects+delta > q.MaxObjects {
		return fmt.Errorf("%w: objects %d/%d", ErrQuotaExceeded, q.UsedObjects+delta, q.MaxObjects)
	}
	return nil
}

// ── Put ─────────────────────────────────────────────────────────────────────

func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, r io.Reader, size int64, opts PutOptions) (repository.Object, error) {
	ctx, span := tracer.Start(ctx, "FileService.Put",
		trace.WithAttributes(
			attribute.String("tenant", tenant),
			attribute.String("bucket", bucket),
			attribute.String("key", key),
			attribute.Int64("content_length", size),
		),
	)
	defer span.End()
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return repository.Object{}, err
	}
	if err := validateMetadata(opts.Metadata); err != nil {
		return repository.Object{}, err
	}
	storeContentHeaders(&opts)
	accessMetadata, err := s.preparePutAccess(ctx, tenant, bucket, key, opts.Metadata)
	if err != nil {
		return repository.Object{}, err
	}
	opts.Metadata = accessMetadata
	if err := ValidateACL(opts.ACL); err != nil {
		return repository.Object{}, err
	}
	if err := s.prepareSSECWrite(&opts); err != nil {
		return repository.Object{}, err
	}
	r, size, cleanup, err := materializeUnknownSize(r, size)
	if err != nil {
		return repository.Object{}, err
	}
	defer cleanup()
	if err := s.preflightQuota(ctx, tenant, 0, 0); err != nil {
		return repository.Object{}, err
	}
	if err := s.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		return repository.Object{}, fmt.Errorf("ensure bucket: %w", err)
	}
	bcfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return repository.Object{}, fmt.Errorf("get bucket config: %w", err)
	}
	if err := s.prepareServerSideEncryption(&opts, bcfg); err != nil {
		return repository.Object{}, err
	}
	if err := s.checkOverwriteProtection(ctx, tenant, bucket, key, bcfg.Versioning); err != nil {
		return repository.Object{}, err
	}
	usage, err := s.objectWriteUsage(ctx, tenant, bucket, key, bcfg.Versioning)
	if err != nil {
		return repository.Object{}, err
	}
	deltaBytes, deltaObjects := usage.deltas(size)
	if err := s.preflightQuota(ctx, tenant, deltaBytes, deltaObjects); err != nil {
		return repository.Object{}, err
	}
	if err := s.preflightBucketQuota(ctx, tenant, bucket, deltaBytes, deltaObjects); err != nil {
		return repository.Object{}, err
	}

	if opts.StorageClass == "" {
		opts.StorageClass = DefaultStorageClass
	}
	storeContentMD5(&opts)
	storeOpts := storage.PutOptions{
		ContentType:       opts.ContentType,
		Metadata:          opts.Metadata,
		SSEAlgorithm:      opts.SSEAlgorithm,
		SSEKMSKeyID:       opts.SSEKMSKeyID,
		SSECustomerKey:    opts.SSECustomerKey,
		SSECustomerKeyMD5: opts.SSECustomerKeyMD5,
	}
	versionID := repository.NewVersionID()
	sk := storageKey(tenant, bucket, key)
	if bcfg.Versioning {
		sk = sk + "@v" + versionID
	}
	sizeReader := &countingReader{reader: r}
	r = sizeReader

	// Wrap the reader for Content-MD5 verification when the header is set.
	if opts.ContentMD5 != "" {
		wrapped, checkFn, err := md5WrapReader(r, opts.ContentMD5)
		if err != nil {
			return repository.Object{}, err
		}
		r = wrapped
		_ = checkFn // deferred until after Put returns
		info, pErr := s.store.Put(ctx, sk, r, size, storeOpts)
		if pErr != nil {
			return repository.Object{}, pErr
		}
		if err := s.validateStoredSize(ctx, sk, size, sizeReader.total, info.Size); err != nil {
			return repository.Object{}, err
		}
		if err := checkFn(); err != nil { // verify Content-MD5
			// Delete the orphaned blob immediately.
			if derr := s.store.Delete(ctx, sk); derr != nil {
				s.logger.Warn("orphaned blob cleanup failed after bad digest", "key", sk, "err", derr)
			}
			return repository.Object{}, err
		}
		obj := s.buildPutObject(key, tenant, bucket, bcfg, opts, sk, versionID, info)
		return s.writePutObject(ctx, obj, bcfg, usage, opts)
	}

	info, err := s.store.Put(ctx, sk, r, size, storeOpts)
	if err != nil {
		return repository.Object{}, err
	}
	if err := s.validateStoredSize(ctx, sk, size, sizeReader.total, info.Size); err != nil {
		return repository.Object{}, err
	}
	obj := s.buildPutObject(key, tenant, bucket, bcfg, opts, sk, versionID, info)
	return s.writePutObject(ctx, obj, bcfg, usage, opts)
}

// buildPutObject constructs a repository.Object from storage info and options.
func (s *FileService) buildPutObject(key string, tenant string, bucket string, bcfg repository.BucketConfig, opts PutOptions, sk string, versionID string, info storage.ObjectInfo) repository.Object {
	metadata := cloneMetadata(opts.Metadata)
	if generation := info.Metadata[storage.GenerationMetadataKey]; generation != "" {
		metadata[storage.GenerationMetadataKey] = generation
	}
	obj := repository.Object{
		TenantID:     tenant,
		Bucket:       bucket,
		Key:          key,
		VersionID:    versionID,
		Backend:      s.store.Backend(),
		StorageKey:   sk,
		Size:         info.Size,
		ETag:         info.ETag,
		ContentType:  opts.ContentType,
		Metadata:     metadata,
		Tags:         opts.Tags,
		StorageClass: opts.StorageClass,
	}
	if bcfg.ObjectLockSeconds > 0 {
		until := time.Now().Add(time.Duration(bcfg.ObjectLockSeconds) * time.Second)
		obj.LockedUntil = &until
	}
	return obj
}

func (s *FileService) writePutObject(
	ctx context.Context,
	obj repository.Object,
	bcfg repository.BucketConfig,
	usage objectWriteUsage,
	opts PutOptions,
) (repository.Object, error) {
	var saved repository.Object
	var err error
	if bcfg.Versioning {
		saved, err = s.repo.InsertObjectVersion(ctx, obj)
	} else {
		saved, err = s.repo.UpsertObject(ctx, obj)
	}
	if err != nil {
		s.logger.Error("repo write failed; storage object orphaned", "tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "err", err)
		return repository.Object{}, fmt.Errorf("repo write: %w", err)
	}
	if opts.ACL != "" {
		if err := s.repo.SetObjectACL(ctx, obj.TenantID, obj.Bucket, obj.Key, opts.ACL); err != nil {
			return repository.Object{}, fmt.Errorf("set object ACL: %w", err)
		}
	}
	if opts.LegalHold {
		if err := s.repo.PutLegalHold(ctx, repository.LegalHold{
			ObjectID: saved.ID, TenantID: saved.TenantID, VersionID: saved.VersionID,
			HoldReason: "s3 upload", CreatedBy: callerFrom(ctx),
		}); err != nil {
			return repository.Object{}, fmt.Errorf("set object legal hold: %w", err)
		}
	}
	if err := s.accountObjectUsage(ctx, obj.TenantID, usage, saved.Size); err != nil {
		return repository.Object{}, err
	}
	s.emit(ctx, saved, repository.EventCreated)
	return saved, nil
}

func (s *FileService) checkOverwriteProtection(ctx context.Context, tenant, bucket, key string, versioning bool) error {
	if versioning {
		return nil // versioning allows overwrite; old version becomes historical
	}
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil
		}
		return err
	}
	return s.checkObjectProtection(ctx, obj)
}

func (s *FileService) checkObjectProtection(ctx context.Context, obj repository.Object) error {
	if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
		return fmt.Errorf("%w: object is retention-locked until %s", ErrLocked, obj.LockedUntil.Format(time.RFC3339))
	}
	if obj.Metadata["_aero_legal_hold"] == "ON" {
		return fmt.Errorf("%w: object is under legal hold", ErrLocked)
	}
	held, err := s.repo.ObjectHasLegalHold(ctx, obj.ID)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("%w: object is under legal hold", ErrLocked)
	}
	return nil
}

func checkCorrupt(obj repository.Object) error {
	if obj.Metadata["_aero_scrub_status"] == "corrupt" {
		return ErrObjectCorrupt
	}
	return nil
}

// storeContentMD5 captures the Content-MD5 header value into the metadata
// under the _aero_content_md5 key for on-read verification and response echo.
func storeContentMD5(opts *PutOptions) {
	if opts.ContentMD5 != "" {
		if opts.Metadata == nil {
			opts.Metadata = map[string]string{}
		}
		opts.Metadata["_aero_content_md5"] = opts.ContentMD5
	}
}

func storeContentHeaders(opts *PutOptions) {
	if opts.ContentDisposition == "" && opts.ContentEncoding == "" {
		return
	}
	opts.Metadata = cloneMetadata(opts.Metadata)
	if opts.ContentDisposition != "" {
		opts.Metadata["_aero_content_disposition"] = opts.ContentDisposition
	}
	if opts.ContentEncoding != "" {
		opts.Metadata["_aero_content_encoding"] = opts.ContentEncoding
	}
}

// md5WrapReader wraps an io.Reader with inline MD5 computation.
// The returned checkFn verifies the computed digest against the provided base64-encoded Content-MD5.
func md5WrapReader(r io.Reader, contentMD5 string) (io.Reader, func() error, error) {
	expected, err := base64.StdEncoding.DecodeString(contentMD5)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: base64 content-md5: %v", ErrInvalidArgs, err)
	}
	h := md5.New()
	tr := io.TeeReader(r, h)
	return tr, func() error {
		computed := h.Sum(nil)
		if !bytes.Equal(computed, expected) {
			return fmt.Errorf("%w: computed %s, sent %s",
				ErrBadDigest, base64.StdEncoding.EncodeToString(computed), contentMD5)
		}
		return nil
	}, nil
}
