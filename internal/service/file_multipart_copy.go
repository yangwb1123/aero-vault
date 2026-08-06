package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// UploadPartCopy copies a range from srcKey into the multipart upload as a
// single part.
func (s *FileService) UploadPartCopy(
	ctx context.Context,
	uploadID string,
	partNumber int32,
	srcKey string,
	srcOffset, length int64,
) (repository.PartRecord, error) {
	return s.UploadPartCopyWithOptions(
		ctx, uploadID, partNumber, "", srcKey, "", srcOffset, length,
		ReadOptions{}, ReadOptions{},
	)
}

func (s *FileService) UploadPartCopyWithOptions(
	ctx context.Context,
	uploadID string,
	partNumber int32,
	srcBucket, srcKey, srcVersionID string,
	srcOffset, length int64,
	source, destination ReadOptions,
) (repository.PartRecord, error) {
	return s.UploadPartCopyFor(
		ctx, defaultMultipartScope(), uploadID, partNumber,
		srcBucket, srcKey, srcVersionID, srcOffset, length, source, destination,
	)
}

// UploadPartCopyFor copies a source range after validating the destination
// upload belongs to the requested tenant, bucket, and key.
func (s *FileService) UploadPartCopyFor(
	ctx context.Context,
	scope MultipartScope,
	uploadID string,
	partNumber int32,
	srcBucket, srcKey, srcVersionID string,
	srcOffset, length int64,
	source, destination ReadOptions,
) (repository.PartRecord, error) {
	if partNumber < 1 || partNumber > 10000 {
		return repository.PartRecord{}, fmt.Errorf(
			"%w: part number must be between 1 and 10000", ErrInvalidArgs,
		)
	}
	upload, err := s.loadMultipartUpload(ctx, scope, uploadID)
	if err != nil {
		return repository.PartRecord{}, err
	}
	if err := s.preflightQuota(ctx, upload.TenantID, 0, 0); err != nil {
		return repository.PartRecord{}, err
	}
	if srcBucket == "" {
		srcBucket = upload.Bucket
	}
	if err := validateSSECRead(upload.Metadata, destination); err != nil {
		return repository.PartRecord{}, err
	}
	src, err := s.copySource(ctx, upload, srcBucket, srcKey, srcVersionID, source)
	if err != nil {
		return repository.PartRecord{}, err
	}
	srcOffset, length, err = normalizeCopyRange(src.Size, srcOffset, length)
	if err != nil {
		return repository.PartRecord{}, err
	}
	if objectUsesSSEC(src.Metadata) || objectUsesSSEC(upload.Metadata) {
		return s.uploadPartCopyStream(ctx, upload, partNumber, src, srcOffset, length, source, destination)
	}
	return s.uploadPartCopyStorage(ctx, upload, partNumber, src, srcOffset, length, source, destination)
}

func (s *FileService) copySource(
	ctx context.Context,
	upload repository.Upload,
	bucket, key, versionID string,
	opts ReadOptions,
) (repository.Object, error) {
	var (
		src repository.Object
		err error
	)
	if versionID == "" {
		src, err = s.repo.GetObject(ctx, upload.TenantID, bucket, key)
	} else {
		src, err = s.repo.GetObjectVersion(ctx, upload.TenantID, bucket, key, versionID)
	}
	if errors.Is(err, repository.ErrNotFound) {
		return repository.Object{}, fmt.Errorf("%w: source %s not found", ErrNotFound, key)
	}
	if err != nil {
		return repository.Object{}, err
	}
	if IsDeleteMarker(src) {
		return repository.Object{}, ErrNotFound
	}
	if err := s.authorizeObject(ctx, access.ActionRead, src); err != nil {
		return repository.Object{}, err
	}
	if err := checkCorrupt(src); err != nil {
		return repository.Object{}, err
	}
	if err := validateSSECRead(src.Metadata, opts); err != nil {
		return repository.Object{}, err
	}
	cfg, err := s.repo.GetBucketConfig(ctx, upload.TenantID, upload.Bucket)
	if err != nil {
		return repository.Object{}, err
	}
	if err := s.checkMultipartLock(ctx, upload, cfg); err != nil {
		return repository.Object{}, err
	}
	return src, nil
}

func normalizeCopyRange(size, offset, length int64) (int64, int64, error) {
	if offset < 0 {
		return -1, size, nil
	}
	if length <= 0 || offset >= size || length > size-offset {
		return 0, 0, fmt.Errorf(
			"%w: copy range offset=%d length=%d size=%d",
			ErrRangeNotSatisfiable, offset, length, size,
		)
	}
	return offset, length, nil
}

func (s *FileService) uploadPartCopyStorage(
	ctx context.Context,
	upload repository.Upload,
	partNumber int32,
	src repository.Object,
	offset, length int64,
	source, destination ReadOptions,
) (repository.PartRecord, error) {
	part, err := s.store.UploadPartCopy(
		ctx, uploadStorageKey(upload), upload.BackendUID,
		partNumber, src.StorageKey, offset, length,
	)
	if errors.Is(err, storage.ErrUnsupported) {
		return s.uploadPartCopyStream(ctx, upload, partNumber, src, offset, length, source, destination)
	}
	if err != nil {
		return repository.PartRecord{}, fmt.Errorf("storage upload part copy: %w", err)
	}
	record := repository.PartRecord{
		UploadID: upload.ID, PartNumber: partNumber, ETag: part.ETag, Size: length,
	}
	if err := s.repo.RecordPart(ctx, record); err != nil {
		return repository.PartRecord{}, fmt.Errorf("repo record part: %w", err)
	}
	return record, nil
}

func (s *FileService) uploadPartCopyStream(
	ctx context.Context,
	upload repository.Upload,
	partNumber int32,
	src repository.Object,
	offset, length int64,
	source, destination ReadOptions,
) (repository.PartRecord, error) {
	rc, _, err := s.GetVersionWithOptions(
		ctx, upload.TenantID, src.Bucket, src.Key, src.VersionID, source,
	)
	if err != nil {
		return repository.PartRecord{}, err
	}
	defer rc.Close()
	reader, actualSize, err := copyRangeReader(rc, offset, length, src.Size)
	if err != nil {
		return repository.PartRecord{}, err
	}
	return s.UploadPartFor(ctx, MultipartScope{
		TenantID: upload.TenantID,
		Bucket:   upload.Bucket,
		Key:      upload.Key,
	}, upload.ID, partNumber, reader, actualSize, destination)
}
