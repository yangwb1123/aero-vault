package service

import (
	"context"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// ListObjectVersionsWithOpts returns one validated page of exact-key versions.
func (s *FileService) ListObjectVersionsWithOpts(
	ctx context.Context,
	tenant, bucket, key string,
	opts repository.VersionListOpts,
) (repository.VersionListPage, error) {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return repository.VersionListPage{}, err
	}
	if err := s.requireActiveTenant(ctx, tenant); err != nil {
		return repository.VersionListPage{}, err
	}
	page, err := s.repo.ListObjectVersionsWithOpts(ctx, tenant, bucket, key, opts)
	if err != nil {
		return repository.VersionListPage{}, err
	}
	page.Versions, err = s.filterAuthorizedVersions(ctx, page.Versions)
	return page, err
}

// ListMultipartUploads returns in-progress uploads scoped to one tenant/bucket.
func (s *FileService) ListMultipartUploads(
	ctx context.Context,
	tenant, bucket, keyMarker, uploadIDMarker string,
	limit int,
) ([]repository.Upload, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.requireActiveTenant(ctx, tenant); err != nil {
		return nil, err
	}
	uploads, err := s.repo.ListUploads(
		ctx, tenant, bucket, keyMarker, uploadIDMarker, limit,
	)
	if err != nil {
		return nil, err
	}
	filtered := make([]repository.Upload, 0, len(uploads))
	for _, upload := range uploads {
		if err := s.authorize(ctx, access.ActionWrite, uploadAccessResource(upload)); err == nil {
			filtered = append(filtered, upload)
		}
	}
	return filtered, nil
}

// BucketHasMultipartUploads reports whether a bucket still has live uploads.
func (s *FileService) BucketHasMultipartUploads(
	ctx context.Context, tenant, bucket string,
) (bool, error) {
	uploads, err := s.ListMultipartUploads(ctx, tenant, bucket, "", "", 1)
	return len(uploads) != 0, err
}
