package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// CompleteMultipartWithParts completes only the explicitly listed parts.
func (s *FileService) CompleteMultipartWithParts(
	ctx context.Context, uploadID string, clientParts []repository.PartRecord,
) (repository.Object, error) {
	return s.CompleteMultipartWithPartsFor(
		ctx, defaultMultipartScope(), uploadID, clientParts, ReadOptions{},
	)
}

func (s *FileService) CompleteMultipartWithPartsAndOptions(
	ctx context.Context,
	uploadID string,
	clientParts []repository.PartRecord,
	opts ReadOptions,
) (repository.Object, error) {
	return s.CompleteMultipartWithPartsFor(
		ctx, defaultMultipartScope(), uploadID, clientParts, opts,
	)
}

// CompleteMultipartWithPartsFor verifies ownership and completes exactly the
// ordered part manifest supplied by the caller.
func (s *FileService) CompleteMultipartWithPartsFor(
	ctx context.Context,
	scope MultipartScope,
	uploadID string,
	clientParts []repository.PartRecord,
	opts ReadOptions,
) (repository.Object, error) {
	return s.completeMultipart(ctx, scope, uploadID, clientParts, true, opts)
}

func (s *FileService) CompleteMultipart(
	ctx context.Context, uploadID string,
) (repository.Object, error) {
	return s.CompleteMultipartFor(
		ctx, defaultMultipartScope(), uploadID, ReadOptions{},
	)
}

func (s *FileService) CompleteMultipartWithOptions(
	ctx context.Context, uploadID string, opts ReadOptions,
) (repository.Object, error) {
	return s.CompleteMultipartFor(ctx, defaultMultipartScope(), uploadID, opts)
}

// CompleteMultipartFor validates ownership and completes all recorded parts.
func (s *FileService) CompleteMultipartFor(
	ctx context.Context,
	scope MultipartScope,
	uploadID string,
	opts ReadOptions,
) (repository.Object, error) {
	return s.completeMultipart(ctx, scope, uploadID, nil, false, opts)
}

func (s *FileService) completeMultipart(
	ctx context.Context,
	scope MultipartScope,
	uploadID string,
	clientParts []repository.PartRecord,
	explicitManifest bool,
	opts ReadOptions,
) (repository.Object, error) {
	scope = normalizeMultipartScope(scope)
	idemKey := multipartCompleteKey(uploadID)
	rec, claimed, err := s.repo.ClaimIdempotencyKey(
		ctx, scope.TenantID, idemKey, "", "",
	)
	if err != nil {
		return repository.Object{}, fmt.Errorf("idempotency claim: %w", err)
	}
	if !claimed {
		return replayMultipartCompletion(rec, scope, uploadID)
	}
	return s.executeMultipartCompletion(
		ctx, scope, uploadID, idemKey, clientParts, explicitManifest, opts,
	)
}

func replayMultipartCompletion(
	rec repository.IdempotencyRecord,
	scope MultipartScope,
	uploadID string,
) (repository.Object, error) {
	if rec.Status != repository.IdempotencyCompleted ||
		rec.ResponseStatus < 200 || rec.ResponseStatus >= 300 ||
		len(rec.ResponseBody) == 0 {
		return repository.Object{}, fmt.Errorf(
			"%w: multipart completion is already in progress", ErrPreconditionFailed,
		)
	}
	var cached repository.Object
	if err := json.Unmarshal(rec.ResponseBody, &cached); err != nil {
		return repository.Object{}, fmt.Errorf("decode completed upload %s: %w", uploadID, err)
	}
	if !multipartScopeMatchesObject(scope, cached) {
		return repository.Object{}, multipartScopeError(scope, uploadID)
	}
	return cached, nil
}

func (s *FileService) executeMultipartCompletion(
	ctx context.Context,
	scope MultipartScope,
	uploadID, idemKey string,
	clientParts []repository.PartRecord,
	explicitManifest bool,
	opts ReadOptions,
) (repository.Object, error) {
	u, err := s.loadMultipartUpload(ctx, scope, uploadID)
	if err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := validateSSECRead(u.Metadata, opts); err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	parts, err := s.multipartCompletionParts(ctx, uploadID, clientParts, explicitManifest)
	if err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	return s.finishMultipartCompletion(ctx, scope, u, parts, idemKey, opts)
}

func (s *FileService) releaseMultipartCompletion(
	ctx context.Context, scope MultipartScope, idemKey string, cause error,
) error {
	if err := s.repo.DeleteIdempotencyKey(ctx, scope.TenantID, idemKey); err != nil {
		s.logger.Warn("release multipart completion claim", "key", idemKey, "err", err)
	}
	return cause
}

func (s *FileService) multipartCompletionParts(
	ctx context.Context,
	uploadID string,
	clientParts []repository.PartRecord,
	explicit bool,
) ([]repository.PartRecord, error) {
	stored, err := s.repo.ListParts(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	if !explicit {
		if len(stored) == 0 {
			return nil, fmt.Errorf("%w: no parts uploaded", ErrInvalidArgs)
		}
		return stored, nil
	}
	return selectMultipartParts(clientParts, stored)
}

func selectMultipartParts(
	client, stored []repository.PartRecord,
) ([]repository.PartRecord, error) {
	if len(client) == 0 {
		return nil, fmt.Errorf("%w: completion manifest is empty", ErrInvalidArgs)
	}
	storedMap := make(map[int32]repository.PartRecord, len(stored))
	for _, part := range stored {
		storedMap[part.PartNumber] = part
	}
	selected := make([]repository.PartRecord, 0, len(client))
	var previous int32
	for _, requested := range client {
		if requested.PartNumber <= previous {
			return nil, fmt.Errorf("%w: parts must be in ascending order", ErrInvalidArgs)
		}
		storedPart, ok := storedMap[requested.PartNumber]
		if !ok {
			return nil, fmt.Errorf(
				"%w: part %d not yet uploaded", ErrInvalidArgs, requested.PartNumber,
			)
		}
		if !strings.EqualFold(
			strings.Trim(requested.ETag, `"`), strings.Trim(storedPart.ETag, `"`),
		) {
			return nil, fmt.Errorf(
				"%w: part %d ETag mismatch", ErrBadDigest, requested.PartNumber,
			)
		}
		selected = append(selected, storedPart)
		previous = requested.PartNumber
	}
	return selected, nil
}

func (s *FileService) finishMultipartCompletion(
	ctx context.Context,
	scope MultipartScope,
	u repository.Upload,
	parts []repository.PartRecord,
	idemKey string,
	opts ReadOptions,
) (repository.Object, error) {
	storageParts, total := buildPartList(parts)
	bcfg, usage, err := s.prepareMultipartCompletion(ctx, u, total)
	if err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	info, err := s.completeStoredMultipart(ctx, u, storageParts, opts)
	if err != nil {
		wrapped := fmt.Errorf("storage complete: %w", err)
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, wrapped)
	}
	saved, err := s.saveMultipartObject(
		ctx, buildObjectFromUpload(u, info, total, bcfg), bcfg,
	)
	if err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := s.persistMultipartCompletion(ctx, u, saved, usage, idemKey); err != nil {
		return repository.Object{}, err
	}
	return saved, nil
}

func (s *FileService) prepareMultipartCompletion(
	ctx context.Context, u repository.Upload, total int64,
) (repository.BucketConfig, objectWriteUsage, error) {
	bcfg, err := s.repo.GetBucketConfig(ctx, u.TenantID, u.Bucket)
	if err != nil {
		return repository.BucketConfig{}, objectWriteUsage{}, fmt.Errorf("get bucket config: %w", err)
	}
	if err := s.checkMultipartLock(ctx, u, bcfg); err != nil {
		return repository.BucketConfig{}, objectWriteUsage{}, err
	}
	usage, err := s.objectWriteUsage(ctx, u.TenantID, u.Bucket, u.Key, bcfg.Versioning)
	if err != nil {
		return repository.BucketConfig{}, objectWriteUsage{}, err
	}
	deltaBytes, deltaObjects := usage.deltas(total)
	if err := s.preflightQuota(ctx, u.TenantID, deltaBytes, deltaObjects); err != nil {
		return repository.BucketConfig{}, objectWriteUsage{}, err
	}
	if err := s.preflightBucketQuota(
		ctx, u.TenantID, u.Bucket, deltaBytes, deltaObjects,
	); err != nil {
		return repository.BucketConfig{}, objectWriteUsage{}, err
	}
	return bcfg, usage, nil
}

func (s *FileService) completeStoredMultipart(
	ctx context.Context,
	u repository.Upload,
	parts []storage.MultipartPart,
	opts ReadOptions,
) (storage.ObjectInfo, error) {
	key := uploadStorageKey(u)
	if objectUsesSSEC(u.Metadata) {
		secure := s.store.(storage.SSECStorage)
		return secure.CompleteMultipartWithOptions(
			ctx, key, u.BackendUID, parts, storagePutOptions(opts),
		)
	}
	return s.store.CompleteMultipart(ctx, key, u.BackendUID, parts)
}

func (s *FileService) persistMultipartCompletion(
	ctx context.Context,
	u repository.Upload,
	saved repository.Object,
	usage objectWriteUsage,
	idemKey string,
) error {
	if err := s.accountObjectUsage(ctx, u.TenantID, usage, saved.Size); err != nil {
		return err
	}
	body, _ := json.Marshal(saved)
	if err := s.repo.CompleteIdempotencyKey(
		ctx, u.TenantID, idemKey, http.StatusOK, body, "application/json", nil,
	); err != nil {
		s.logger.Warn("cache multipart completion", "upload_id", u.ID, "err", err)
	}
	if err := s.repo.DeleteUpload(ctx, u.ID); err != nil {
		s.logger.Warn("delete completed multipart upload", "upload_id", u.ID, "err", err)
	}
	s.emit(ctx, saved, repository.EventCreated)
	return nil
}

func buildPartList(
	parts []repository.PartRecord,
) ([]storage.MultipartPart, int64) {
	storageParts := make([]storage.MultipartPart, 0, len(parts))
	var total int64
	for _, part := range parts {
		storageParts = append(storageParts, storage.MultipartPart{
			PartNumber: part.PartNumber,
			ETag:       part.ETag,
		})
		total += part.Size
	}
	return storageParts, total
}
