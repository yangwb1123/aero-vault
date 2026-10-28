package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AbortMultipartFor verifies ownership and persists an idempotent abort result.
func (s *FileService) AbortMultipartFor(ctx context.Context, scope MultipartScope, uploadID string) error {
	scope = normalizeMultipartScope(scope)
	idemKey := multipartAbortKey(uploadID)
	rec, claimed, err := s.repo.ClaimIdempotencyKey(ctx, scope.TenantID, idemKey, "", "")
	if err != nil {
		return fmt.Errorf("idempotency claim: %w", err)
	}
	if !claimed {
		return s.replayMultipartAbort(ctx, rec, scope, uploadID)
	}
	return s.executeMultipartAbort(ctx, scope, uploadID, idemKey)
}

func (s *FileService) executeMultipartAbort(ctx context.Context, scope MultipartScope, uploadID, idemKey string) error {
	u, err := s.loadMultipartUpload(ctx, scope, uploadID)
	if err != nil {
		return s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := s.store.AbortMultipart(ctx, uploadStorageKey(u), u.BackendUID); err != nil {
		return s.releaseMultipartCompletion(ctx, scope, idemKey, fmt.Errorf("storage abort multipart: %w", err))
	}
	body, err := json.Marshal(u)
	if err != nil {
		return s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := s.repo.CompleteIdempotencyKey(ctx, scope.TenantID, idemKey, http.StatusNoContent, body, "application/json", nil); err != nil {
		return s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := s.repo.DeleteUpload(ctx, uploadID); err != nil {
		_ = s.repo.DeleteIdempotencyKey(ctx, scope.TenantID, idemKey)
		return err
	}
	return nil
}
