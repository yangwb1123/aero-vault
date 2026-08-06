package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// InitMultipart opens a multipart upload and persists the session.
func (s *FileService) InitMultipart(ctx context.Context, tenant, bucket, key string, opts PutOptions) (repository.Upload, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return repository.Upload{}, err
	}
	if opts.StorageClass == "" {
		opts.StorageClass = DefaultStorageClass
	}
	if err := validateMetadata(opts.Metadata); err != nil {
		return repository.Upload{}, err
	}
	storeContentHeaders(&opts)
	accessMetadata, err := s.preparePutAccess(ctx, tenant, bucket, key, opts.Metadata)
	if err != nil {
		return repository.Upload{}, err
	}
	opts.Metadata = accessMetadata
	if err := ValidateACL(opts.ACL); err != nil {
		return repository.Upload{}, err
	}
	if opts.ACL != "" {
		opts.Metadata = cloneMetadata(opts.Metadata)
		opts.Metadata[pendingACLMetadataKey] = opts.ACL
	}
	if opts.LegalHold {
		opts.Metadata = cloneMetadata(opts.Metadata)
		opts.Metadata[pendingLegalHoldKey] = "ON"
	}
	if err := s.prepareSSECWrite(&opts); err != nil {
		return repository.Upload{}, err
	}
	if err := s.preflightQuota(ctx, tenant, 0, 0); err != nil {
		return repository.Upload{}, err
	}
	if err := s.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		return repository.Upload{}, err
	}
	bcfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return repository.Upload{}, fmt.Errorf("get bucket config: %w", err)
	}
	if err := s.prepareServerSideEncryption(&opts, bcfg); err != nil {
		return repository.Upload{}, err
	}
	// On a versioned bucket, assemble into a unique per-version storage key so the
	// completed object becomes a new version instead of overwriting the current one
	// (mirrors Put's one-blob-per-version scheme).
	sk := storageKey(tenant, bucket, key)
	versionID := repository.NewVersionID()
	if bcfg.Versioning {
		sk = sk + "@v" + versionID
	}
	storeContentMD5(&opts)
	init, err := s.store.InitMultipart(ctx, sk, storage.PutOptions{
		ContentType:       opts.ContentType,
		Metadata:          opts.Metadata,
		SSEAlgorithm:      opts.SSEAlgorithm,
		SSEKMSKeyID:       opts.SSEKMSKeyID,
		SSECustomerKey:    opts.SSECustomerKey,
		SSECustomerKeyMD5: opts.SSECustomerKeyMD5,
	})
	if err != nil {
		return repository.Upload{}, fmt.Errorf("storage init multipart: %w", err)
	}
	u := repository.Upload{
		ID:           init.UploadID,
		TenantID:     tenant,
		Bucket:       bucket,
		Key:          key,
		VersionID:    versionID,
		Backend:      s.store.Backend(),
		BackendUID:   init.UploadID,
		StorageKey:   sk,
		ContentType:  opts.ContentType,
		Metadata:     opts.Metadata,
		Tags:         opts.Tags,
		StorageClass: opts.StorageClass,
	}
	if err := s.repo.CreateUpload(ctx, u); err != nil {
		_ = s.store.AbortMultipart(ctx, sk, init.UploadID)
		return repository.Upload{}, fmt.Errorf("repo create upload: %w", err)
	}
	return u, nil
}

func (s *FileService) UploadPart(ctx context.Context, uploadID string, partNumber int32, r io.Reader, size int64) (repository.PartRecord, error) {
	return s.UploadPartFor(ctx, defaultMultipartScope(), uploadID, partNumber, r, size, ReadOptions{})
}

func (s *FileService) UploadPartWithOptions(ctx context.Context, uploadID string, partNumber int32, r io.Reader, size int64, opts ReadOptions) (repository.PartRecord, error) {
	return s.UploadPartFor(ctx, defaultMultipartScope(), uploadID, partNumber, r, size, opts)
}

// UploadPartFor uploads or replaces one part after verifying upload ownership.
func (s *FileService) UploadPartFor(
	ctx context.Context,
	scope MultipartScope,
	uploadID string,
	partNumber int32,
	r io.Reader,
	size int64,
	opts ReadOptions,
) (repository.PartRecord, error) {
	u, err := s.loadMultipartUpload(ctx, scope, uploadID)
	if err != nil {
		return repository.PartRecord{}, err
	}
	if err := s.preflightQuota(ctx, u.TenantID, 0, 0); err != nil {
		return repository.PartRecord{}, err
	}
	if err := validateSSECRead(u.Metadata, opts); err != nil {
		return repository.PartRecord{}, err
	}
	if partNumber < 1 || partNumber > 10000 {
		return repository.PartRecord{}, fmt.Errorf(
			"%w: part number must be between 1 and 10000", ErrInvalidArgs,
		)
	}
	r, size, cleanup, err := materializeUnknownSize(r, size)
	if err != nil {
		return repository.PartRecord{}, err
	}
	defer cleanup()
	sizeReader := &countingReader{reader: r}
	r = sizeReader

	sk := uploadStorageKey(u)
	var part storage.MultipartPart
	if objectUsesSSEC(u.Metadata) {
		secure := s.store.(storage.SSECStorage)
		part, err = secure.UploadPartWithOptions(ctx, sk, u.BackendUID, partNumber, r, size, storagePutOptions(opts))
	} else {
		part, err = s.store.UploadPart(ctx, sk, u.BackendUID, partNumber, r, size)
	}
	if err != nil {
		return repository.PartRecord{}, fmt.Errorf("storage upload part: %w", err)
	}
	if err := s.validateConsumedSize(
		ctx, "multipart part", size, sizeReader.total,
	); err != nil {
		return repository.PartRecord{}, err
	}
	pr := repository.PartRecord{
		UploadID:   uploadID,
		PartNumber: partNumber,
		ETag:       part.ETag,
		Size:       size,
	}
	if err := s.repo.RecordPart(ctx, pr); err != nil {
		return repository.PartRecord{}, fmt.Errorf("repo record part: %w", err)
	}
	return pr, nil
}

func (s *FileService) checkMultipartLock(ctx context.Context, u repository.Upload, bcfg repository.BucketConfig) error {
	if bcfg.Versioning {
		return nil
	}
	cur, err := s.repo.GetObject(ctx, u.TenantID, u.Bucket, u.Key)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.checkObjectProtection(ctx, cur)
}

func uploadAccessResource(upload repository.Upload) access.Resource {
	return access.Resource{
		TenantID: upload.TenantID, Bucket: upload.Bucket, Key: upload.Key,
		Kind: objectResourceKind(upload.Key), OwnerID: upload.Metadata[ownerMetadataKey],
	}
}

func buildObjectFromUpload(u repository.Upload, info storage.ObjectInfo, total int64, bcfg repository.BucketConfig) repository.Object {
	if info.Size == 0 {
		info.Size = total
	}
	if info.ContentType == "" {
		info.ContentType = u.ContentType
	}
	obj := repository.Object{
		TenantID:     u.TenantID,
		Bucket:       u.Bucket,
		Key:          u.Key,
		VersionID:    u.VersionID,
		Backend:      u.Backend,
		StorageKey:   u.StorageKey,
		Size:         info.Size,
		ETag:         info.ETag,
		ContentType:  info.ContentType,
		Metadata:     u.Metadata,
		Tags:         u.Tags,
		StorageClass: u.StorageClass,
	}
	if bcfg.ObjectLockSeconds > 0 {
		until := time.Now().Add(time.Duration(bcfg.ObjectLockSeconds) * time.Second)
		obj.LockedUntil = &until
	}
	return obj
}

func (s *FileService) saveMultipartObject(ctx context.Context, obj repository.Object, bcfg repository.BucketConfig) (repository.Object, error) {
	acl := obj.Metadata[pendingACLMetadataKey]
	legalHold := obj.Metadata[pendingLegalHoldKey] == "ON"
	if acl != "" || legalHold {
		obj.Metadata = cloneMetadata(obj.Metadata)
		delete(obj.Metadata, pendingACLMetadataKey)
		delete(obj.Metadata, pendingLegalHoldKey)
	}
	var (
		saved repository.Object
		err   error
	)
	if bcfg.Versioning {
		saved, err = s.repo.InsertObjectVersion(ctx, obj)
	} else {
		saved, err = s.repo.UpsertObject(ctx, obj)
	}
	if err != nil {
		s.logger.Error("repo write after multipart failed; orphan", "key", obj.Key, "err", err)
		return repository.Object{}, fmt.Errorf("repo write: %w", err)
	}
	if acl != "" {
		if err := s.repo.SetObjectACL(ctx, obj.TenantID, obj.Bucket, obj.Key, acl); err != nil {
			return repository.Object{}, fmt.Errorf("set multipart object ACL: %w", err)
		}
	}
	if legalHold {
		if err := s.repo.PutLegalHold(ctx, repository.LegalHold{
			ObjectID: saved.ID, TenantID: saved.TenantID, VersionID: saved.VersionID,
			HoldReason: "s3 multipart upload", CreatedBy: callerFrom(ctx),
		}); err != nil {
			return repository.Object{}, fmt.Errorf("set multipart object legal hold: %w", err)
		}
	}
	return saved, nil
}

func (s *FileService) AbortMultipart(ctx context.Context, uploadID string) error {
	return s.AbortMultipartFor(ctx, defaultMultipartScope(), uploadID)
}

// AbortMultipartFor verifies ownership and persists an idempotent abort result.
func (s *FileService) AbortMultipartFor(
	ctx context.Context, scope MultipartScope, uploadID string,
) error {
	scope = normalizeMultipartScope(scope)
	idemKey := multipartAbortKey(uploadID)
	rec, claimed, err := s.repo.ClaimIdempotencyKey(
		ctx, scope.TenantID, idemKey, "", "",
	)
	if err != nil {
		return fmt.Errorf("idempotency claim: %w", err)
	}
	if !claimed {
		if rec.Status != repository.IdempotencyCompleted ||
			rec.ResponseStatus != http.StatusNoContent {
			return fmt.Errorf("%w: multipart abort is already in progress", ErrPreconditionFailed)
		}
		var cached repository.Upload
		if err := json.Unmarshal(rec.ResponseBody, &cached); err != nil {
			return fmt.Errorf("decode aborted upload %s: %w", uploadID, err)
		}
		if !multipartScopeMatchesUpload(scope, cached) {
			return multipartScopeError(scope, uploadID)
		}
		return nil
	}
	u, err := s.loadMultipartUpload(ctx, scope, uploadID)
	if err != nil {
		return s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := s.store.AbortMultipart(ctx, uploadStorageKey(u), u.BackendUID); err != nil {
		return s.releaseMultipartCompletion(
			ctx, scope, idemKey, fmt.Errorf("storage abort multipart: %w", err),
		)
	}
	body, _ := json.Marshal(u)
	if err := s.repo.CompleteIdempotencyKey(
		ctx, scope.TenantID, idemKey, http.StatusNoContent, body, "application/json", nil,
	); err != nil {
		return s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := s.repo.DeleteUpload(ctx, uploadID); err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, scope.TenantID, idemKey)
		return err
	}
	return nil
}

// multipartCompleteKey returns the idempotency key for CompleteMultipart.
func multipartCompleteKey(uploadID string) string {
	return "_mp_complete:" + uploadID
}

// multipartAbortKey returns the idempotency key for AbortMultipart.
func multipartAbortKey(uploadID string) string {
	return "_mp_abort:" + uploadID
}
