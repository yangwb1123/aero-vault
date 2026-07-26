package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

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
		// Unique, collision-free per-version suffix (see Put). The authoritative
		// version_id is assigned by InsertObjectVersion at CompleteMultipart.
		sk = sk + "@v" + repository.NewVersionID()
	}
	storeContentMD5(&opts)
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
	// Check that the upload exists and hasn't been completed/aborted.
	u, err := s.repo.GetUpload(ctx, uploadID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			return repository.PartRecord{}, ErrUploadNotFound
		}
		return repository.PartRecord{}, err
	}
	// Check if this part was already uploaded (UploadPart idempotency).
	// The key is formed as "_mp_part:<uploadID>:<partNumber>" to reuse the
	// existing idempotency_keys table without schema changes.
	idemKey := multipartPartKey(uploadID, partNumber)
	idemRec, claimed, err := s.repo.ClaimIdempotencyKey(ctx, u.TenantID, idemKey, "", "")
	if err != nil {
		return repository.PartRecord{}, fmt.Errorf("idempotency claim: %w", err)
	}
	if !claimed {
		// Already uploaded: return the cached part record.
		if idemRec.ResponseStatus == http.StatusOK && len(idemRec.ResponseBody) > 0 {
			var cached repository.PartRecord
			if err := json.Unmarshal(idemRec.ResponseBody, &cached); err == nil {
				return cached, nil
			}
		}
		// Fall through to re-upload if we can't deserialize the cache.
	}

	sk := uploadStorageKey(u)
	part, err := s.store.UploadPart(ctx, sk, u.BackendUID, partNumber, r, size)
	if err != nil {
		// Release the claim on storage error.
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.PartRecord{}, fmt.Errorf("storage upload part: %w", err)
	}
	pr := repository.PartRecord{
		UploadID:   uploadID,
		PartNumber: partNumber,
		ETag:       part.ETag,
		Size:       size,
	}
	if err := s.repo.RecordPart(ctx, pr); err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.PartRecord{}, fmt.Errorf("repo record part: %w", err)
	}
	// Cache the successful result.
	body, _ := json.Marshal(pr)
	_ = s.repo.CompleteIdempotencyKey(ctx, u.TenantID, idemKey, http.StatusOK, body, "application/json", nil)
	return pr, nil
}

// UploadPartCopy copies a range from srcKey into the multipart upload as a single
// part, using server-side transfer when the storage backend supports it.
func (s *FileService) UploadPartCopy(ctx context.Context, uploadID string, partNumber int32, srcKey string, srcOffset, length int64) (repository.PartRecord, error) {
	u, err := s.repo.GetUpload(ctx, uploadID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			return repository.PartRecord{}, ErrUploadNotFound
		}
		return repository.PartRecord{}, err
	}

	// Verify the source exists and belongs to the same tenant.
	srcObj, err := s.repo.GetObject(ctx, u.TenantID, u.Bucket, srcKey)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return repository.PartRecord{}, fmt.Errorf("%w: source %s not found", ErrNotFound, srcKey)
		}
		return repository.PartRecord{}, err
	}
	// Validate bucket config for source (WORM check is done at write time).
	bcfg, err := s.repo.GetBucketConfig(ctx, u.TenantID, u.Bucket)
	if err != nil {
		return repository.PartRecord{}, err
	}
	if err := s.checkMultipartLock(ctx, u, bcfg); err != nil {
		return repository.PartRecord{}, err
	}

	// Build the storage key for the upload's backend UID so UploadPartCopy knows
	// where to place the part. Use the source object's stored StorageKey which
	// already includes the correct version suffix (or not) based on whether
	// versioning is enabled at the time of PUT.
	sk := uploadStorageKey(u)
	srcSK := srcObj.StorageKey

	part, err := s.store.UploadPartCopy(ctx, sk, u.BackendUID, partNumber, srcSK, srcOffset, length)
	if err != nil {
		if errors.Is(err, storage.ErrUnsupported) {
			// Backend doesn't support server-side part copy; fall through to
			// client-stream copy via Get+UploadPart.
			return s.uploadPartCopyStream(ctx, uploadID, partNumber, srcObj, srcSK, srcOffset, length)
		}
		return repository.PartRecord{}, fmt.Errorf("storage upload part copy: %w", err)
	}

	pr := repository.PartRecord{
		UploadID:   uploadID,
		PartNumber: partNumber,
		ETag:       part.ETag,
		Size:       length,
	}
	if err := s.repo.RecordPart(ctx, pr); err != nil {
		return repository.PartRecord{}, fmt.Errorf("repo record part: %w", err)
	}
	return pr, nil
}

func (s *FileService) uploadPartCopyStream(ctx context.Context, uploadID string, partNumber int32, srcObj repository.Object, srcSK string, srcOffset, length int64) (repository.PartRecord, error) {
	rc, _, err := s.store.Get(ctx, srcSK)
	if err != nil {
		return repository.PartRecord{}, err
	}
	defer rc.Close()

	var r io.Reader = rc
	var actualSize int64 = srcObj.Size
	if srcOffset >= 0 {
		// Seek not possible on stream; drain to memory for the range.
		buf := make([]byte, length)
		// Skip srcOffset bytes first.
		if _, err := io.CopyN(io.Discard, r, srcOffset); err != nil {
			return repository.PartRecord{}, err
		}
		if _, err := io.ReadFull(r, buf); err != nil {
			return repository.PartRecord{}, err
		}
		r = bytes.NewReader(buf)
		actualSize = length
	}

	return s.UploadPart(ctx, uploadID, partNumber, r, actualSize)
}

// CompleteMultipartWithParts completes a multipart upload, verifying that the
// client-supplied part ETags match the server-stored parts. This catches bit
// rot / partial writes that the basic CompleteMultipart would miss.
func (s *FileService) CompleteMultipartWithParts(ctx context.Context, uploadID string, clientParts []repository.PartRecord) (repository.Object, error) {
	// Verify client ETags against server-stored parts before completing.
	stored, err := s.repo.ListParts(ctx, uploadID)
	if err != nil {
		return repository.Object{}, err
	}
	if err := verifyMultipartETags(clientParts, stored); err != nil {
		return repository.Object{}, err
	}
	// ETags match; proceed with the standard completion logic.
	return s.CompleteMultipart(ctx, uploadID)
}

// verifyMultipartETags checks that every client-supplied part has a matching
// server-stored part with the same PartNumber and ETag.
func verifyMultipartETags(client, stored []repository.PartRecord) error {
	// Build a map of stored parts for O(n) lookup by part number.
	storedMap := make(map[int32]string, len(stored))
	for _, p := range stored {
		storedMap[p.PartNumber] = p.ETag
	}
	// Verify every client-specified part exists on the server with a matching
	// ETag. The client may request fewer parts than were uploaded (the server
	// only includes the listed parts; extras are GC'd).
	for _, cp := range client {
		se, ok := storedMap[cp.PartNumber]
		if !ok {
			return fmt.Errorf("%w: part %d not yet uploaded", ErrInvalidArgs, cp.PartNumber)
		}
		clientETag := strings.Trim(cp.ETag, `"`)
		storedETag := strings.Trim(se, `"`)
		if !strings.EqualFold(clientETag, storedETag) {
			return fmt.Errorf("%w: part %d ETag mismatch: client %q, server %q",
				ErrBadDigest, cp.PartNumber, cp.ETag, se)
		}
	}
	return nil
}

func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
	u, err := s.repo.GetUpload(ctx, uploadID)
	if errors.Is(err, repository.ErrUploadNotFound) {
		return repository.Object{}, ErrUploadNotFound
	}
	if err != nil {
		return repository.Object{}, err
	}

	// CompleteMultipart idempotency: check if this upload was already completed.
	// The key is "_mp_complete:<uploadID>" to reuse the idempotency_keys table.
	idemKey := multipartCompleteKey(uploadID)
	idemRec, claimed, idemErr := s.repo.ClaimIdempotencyKey(ctx, u.TenantID, idemKey, "", "")
	if idemErr != nil {
		return repository.Object{}, fmt.Errorf("idempotency claim: %w", idemErr)
	}
	if !claimed {
		// Already completed: replay cached result.
		if idemRec.ResponseStatus >= 200 && idemRec.ResponseStatus < 300 && len(idemRec.ResponseBody) > 0 {
			var cached repository.Object
			if err := json.Unmarshal(idemRec.ResponseBody, &cached); err == nil {
				return cached, nil
			}
		}
		// If we can't deserialize, fall through to re-execute.
	}

	parts, err := s.repo.ListParts(ctx, uploadID)
	if err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.Object{}, err
	}
	if len(parts) == 0 {
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.Object{}, fmt.Errorf("%w: no parts uploaded", ErrInvalidArgs)
	}
	storageParts, total := buildPartList(parts)

	bcfg, err := s.repo.GetBucketConfig(ctx, u.TenantID, u.Bucket)
	if err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.Object{}, fmt.Errorf("get bucket config: %w", err)
	}
	if err := s.preflightMultipartQuota(ctx, u.TenantID, total); err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.Object{}, err
	}
	if err := s.checkMultipartLock(ctx, u, bcfg); err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.Object{}, err
	}

	sk := uploadStorageKey(u)
	info, err := s.store.CompleteMultipart(ctx, sk, u.BackendUID, storageParts)
	if err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.Object{}, fmt.Errorf("storage complete: %w", err)
	}

	obj := buildObjectFromUpload(u, info, total, bcfg)
	saved, err := s.saveMultipartObject(ctx, obj, bcfg)
	if err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, u.TenantID, idemKey)
		return repository.Object{}, err
	}
	// Cache the successful result for idempotent replay.
	body, _ := json.Marshal(saved)
	_ = s.repo.CompleteIdempotencyKey(ctx, u.TenantID, idemKey, http.StatusOK, body, "application/json", nil)

	// Account the upload against the tenant quota (best effort).
	s.trackMultipartUsage(ctx, u.TenantID, saved.Size)
	_ = s.repo.DeleteUpload(ctx, uploadID)
	s.emit(ctx, saved, repository.EventCreated)
	return saved, nil
}

func (s *FileService) trackMultipartUsage(ctx context.Context, tenant string, size int64) {
	if _, qErr := s.repo.AddTenantUsage(ctx, tenant, size, 1); qErr != nil {
		s.logger.Warn("quota usage increment failed", "tenant", tenant, "err", qErr)
	}
}

func buildPartList(parts []repository.PartRecord) ([]storage.MultipartPart, int64) {
	storageParts := make([]storage.MultipartPart, 0, len(parts))
	var total int64
	for _, p := range parts {
		storageParts = append(storageParts, storage.MultipartPart{PartNumber: p.PartNumber, ETag: p.ETag})
		total += p.Size
	}
	return storageParts, total
}

func (s *FileService) preflightMultipartQuota(ctx context.Context, tenant string, total int64) error {
	if q, qErr := s.repo.GetTenantQuota(ctx, tenant); qErr == nil {
		if q.MaxBytes > 0 && q.UsedBytes+total > q.MaxBytes {
			return fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, q.UsedBytes+total, q.MaxBytes)
		}
		if q.MaxObjects > 0 && q.UsedObjects+1 > q.MaxObjects {
			return fmt.Errorf("%w: objects %d/%d", ErrQuotaExceeded, q.UsedObjects+1, q.MaxObjects)
		}
	}
	return nil
}

func (s *FileService) checkMultipartLock(ctx context.Context, u repository.Upload, bcfg repository.BucketConfig) error {
	if !bcfg.Versioning {
		if cur, gErr := s.repo.GetObject(ctx, u.TenantID, u.Bucket, u.Key); gErr == nil {
			if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
				return fmt.Errorf("%w: overwrite blocked until %s", ErrLocked, cur.LockedUntil.Format(time.RFC3339))
			}
		}
	}
	return nil
}

func buildObjectFromUpload(u repository.Upload, info storage.ObjectInfo, total int64, bcfg repository.BucketConfig) repository.Object {
	if info.Size == 0 {
		info.Size = total
	}
	obj := repository.Object{
		TenantID:    u.TenantID,
		Bucket:      u.Bucket,
		Key:         u.Key,
		Backend:     u.Backend,
		StorageKey:  u.StorageKey,
		Size:        info.Size,
		ETag:        info.ETag,
		ContentType: info.ContentType,
		Metadata:    u.Metadata,
	}
	if bcfg.ObjectLockSeconds > 0 {
		until := time.Now().Add(time.Duration(bcfg.ObjectLockSeconds) * time.Second)
		obj.LockedUntil = &until
	}
	return obj
}

func (s *FileService) saveMultipartObject(ctx context.Context, obj repository.Object, bcfg repository.BucketConfig) (repository.Object, error) {
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
	if bcfg.ObjectLockSeconds > 0 && saved.LockedUntil == nil {
		until := time.Now().Add(time.Duration(bcfg.ObjectLockSeconds) * time.Second)
		_ = s.repo.SetLockedUntil(ctx, obj.TenantID, obj.Bucket, obj.Key, until)
		saved.LockedUntil = &until
	}
	return saved, nil
}

func (s *FileService) AbortMultipart(ctx context.Context, uploadID string) error {
	// Check if this upload was already aborted (AbortMultipart idempotency).
	idemKey := multipartAbortKey(uploadID)
	rec, claimed, idemErr := s.repo.ClaimIdempotencyKey(ctx, "_system", idemKey, "", "")
	if idemErr == nil && !claimed && rec.ResponseStatus == http.StatusNoContent {
		return nil // Already aborted, idempotent success.
	}

	u, err := s.repo.GetUpload(ctx, uploadID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			// If the upload doesn't exist but we've already aborted it, return success.
			if idemErr == nil && !claimed {
				return nil
			}
			return ErrUploadNotFound
		}
		return err
	}
	sk := uploadStorageKey(u)
	_ = s.store.AbortMultipart(ctx, sk, u.BackendUID)
	if err := s.repo.DeleteUpload(ctx, uploadID); err != nil {
		return err
	}
	// Cache the abort result for idempotent replay.
	_ = s.repo.CompleteIdempotencyKey(ctx, "_system", idemKey, http.StatusNoContent, nil, "", nil)
	return nil
}

// multipartCompleteKey returns the idempotency key for CompleteMultipart.
func multipartCompleteKey(uploadID string) string {
	return "_mp_complete:" + uploadID
}

// multipartPartKey returns the idempotency key for UploadPart.
func multipartPartKey(uploadID string, partNumber int32) string {
	return fmt.Sprintf("_mp_part:%s:%d", uploadID, partNumber)
}

// multipartAbortKey returns the idempotency key for AbortMultipart.
func multipartAbortKey(uploadID string) string {
	return "_mp_abort:" + uploadID
}
