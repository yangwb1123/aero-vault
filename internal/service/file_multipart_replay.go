package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func validateMultipartRecord(rec repository.IdempotencyRecord, scope MultipartScope, key string) error {
	if rec.TenantID != scope.TenantID || rec.Key != key || rec.Fingerprint != "" {
		return spaceBoundaryFault(errors.New("multipart replay record scope conflict"))
	}
	return nil
}

// replayMultipartCompletion decodes only the original persisted object. Neither
// a retry body nor a latest-key lookup can supply missing scope or identity.
func replayMultipartCompletion(rec repository.IdempotencyRecord, scope MultipartScope, uploadID string) (repository.Object, error) {
	if err := spaces.ValidateIdentifier(uploadID); err != nil {
		return repository.Object{}, spaceBoundaryFault(err)
	}
	if err := validateMultipartRecord(rec, scope, multipartCompleteKey(uploadID)); err != nil {
		return repository.Object{}, err
	}
	if rec.Status != repository.IdempotencyCompleted || rec.ResponseStatus != http.StatusOK || len(rec.ResponseBody) == 0 {
		return repository.Object{}, fmt.Errorf("%w: multipart completion is already in progress", ErrPreconditionFailed)
	}
	var cached repository.Object
	if err := json.Unmarshal(rec.ResponseBody, &cached); err != nil {
		return repository.Object{}, spaceBoundaryFault(err)
	}
	if err := validateReplayObject(cached); err != nil {
		return repository.Object{}, err
	}
	if !multipartScopeMatchesObject(scope, cached) {
		return repository.Object{}, spaceBoundaryFault(multipartScopeError(scope, uploadID))
	}
	return cached, nil
}

func (s *FileService) replayMultipartCompletion(ctx context.Context, rec repository.IdempotencyRecord, scope MultipartScope, uploadID string, opts ReadOptions) (repository.Object, error) {
	cached, err := replayMultipartCompletion(rec, scope, uploadID)
	if err != nil {
		return repository.Object{}, err
	}
	if err := s.AuthorizeReplayObject(ctx, access.ActionWrite, cached, opts); err != nil {
		return repository.Object{}, err
	}
	return cached, nil
}

func (s *FileService) replayMultipartAbort(ctx context.Context, rec repository.IdempotencyRecord, scope MultipartScope, uploadID string) error {
	if err := validateMultipartRecord(rec, scope, multipartAbortKey(uploadID)); err != nil {
		return err
	}
	if rec.Status != repository.IdempotencyCompleted || rec.ResponseStatus != http.StatusNoContent {
		return fmt.Errorf("%w: multipart abort is already in progress", ErrPreconditionFailed)
	}
	var cached repository.Upload
	if err := json.Unmarshal(rec.ResponseBody, &cached); err != nil {
		return spaceBoundaryFault(err)
	}
	if err := validateReplayUpload(cached, uploadID); err != nil {
		return err
	}
	if !multipartScopeMatchesUpload(scope, cached) {
		return spaceBoundaryFault(multipartScopeError(scope, uploadID))
	}
	return s.authorize(ctx, access.ActionWrite, uploadAccessResource(cached))
}

func validateReplayUpload(u repository.Upload, requestedID string) error {
	if err := spaces.ValidateBindingInput(u.TenantID, u.Bucket); err != nil {
		return spaceBoundaryFault(err)
	}
	if err := validateKey(u.Key); err != nil {
		return spaceBoundaryFault(err)
	}
	if u.ID == "" || u.ID != requestedID || u.BackendUID == "" || u.Backend == "" || u.VersionID == "" {
		return spaceBoundaryFault(errors.New("incomplete multipart upload identity"))
	}
	return nil
}
