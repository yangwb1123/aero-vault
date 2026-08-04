package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// MultipartScope binds an opaque upload ID to the tenant and, when available,
// bucket/key from the protocol route. Empty Bucket and Key fields are ignored.
type MultipartScope struct {
	TenantID string
	Bucket   string
	Key      string
}

func defaultMultipartScope() MultipartScope {
	return MultipartScope{TenantID: DefaultTenant}
}

func normalizeMultipartScope(scope MultipartScope) MultipartScope {
	if scope.TenantID == "" {
		scope.TenantID = DefaultTenant
	}
	return scope
}

func (s *FileService) loadMultipartUpload(
	ctx context.Context, scope MultipartScope, uploadID string,
) (repository.Upload, error) {
	upload, err := s.repo.GetUpload(ctx, uploadID)
	if errors.Is(err, repository.ErrUploadNotFound) {
		return repository.Upload{}, ErrUploadNotFound
	}
	if err != nil {
		return repository.Upload{}, err
	}
	if !multipartScopeMatchesUpload(normalizeMultipartScope(scope), upload) {
		return repository.Upload{}, ErrUploadNotFound
	}
	if err := s.authorize(ctx, access.ActionWrite, uploadAccessResource(upload)); err != nil {
		return repository.Upload{}, err
	}
	return upload, nil
}

func multipartScopeMatchesUpload(scope MultipartScope, upload repository.Upload) bool {
	if upload.TenantID != scope.TenantID {
		return false
	}
	if scope.Bucket != "" && upload.Bucket != scope.Bucket {
		return false
	}
	return scope.Key == "" || upload.Key == scope.Key
}

func multipartScopeMatchesObject(scope MultipartScope, obj repository.Object) bool {
	if obj.TenantID != scope.TenantID {
		return false
	}
	if scope.Bucket != "" && obj.Bucket != scope.Bucket {
		return false
	}
	return scope.Key == "" || obj.Key == scope.Key
}

func multipartScopeError(scope MultipartScope, uploadID string) error {
	return fmt.Errorf(
		"%w: upload %s is not in tenant %s",
		ErrUploadNotFound, uploadID, normalizeMultipartScope(scope).TenantID,
	)
}

// ListMultipartParts validates upload ownership before exposing its parts.
func (s *FileService) ListMultipartParts(
	ctx context.Context, scope MultipartScope, uploadID string,
) ([]repository.PartRecord, error) {
	if _, err := s.loadMultipartUpload(ctx, scope, uploadID); err != nil {
		return nil, err
	}
	return s.repo.ListParts(ctx, uploadID)
}
