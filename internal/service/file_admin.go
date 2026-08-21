package service

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type deleteRefs struct {
	ShareIDs     []string
	VersionCount int
	ChunkCount   int
}

// AdminDelete removes one object through the privileged vault.file.delete
// action. It intentionally skips write-side quota preflight: the operation
// only releases usage, while the existing transactional delete path still
// owns storage, cascade, audit, outbox, and usage ordering.
func (s *FileService) AdminDelete(ctx context.Context, tenant, bucket, key string, hard bool) error {
	ctx = WithDeletePermission(ctx, access.PermissionVaultFileDelete)
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return err
	}
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	action := access.ActionAdminDelete
	// ACCESS_DELETE_FAIL_CLOSED=false is the documented legacy opt-out for
	// the service gate when access control is disabled. The REST admin boundary
	// has already performed its own attributable-principal check; preserving
	// the ordinary delete action here keeps that opt-out effective for an
	// authenticated operator while ActionAdminDelete remains strict whenever
	// a real provider is configured.
	if s.authorizer == nil && s.deleteFailOpen {
		action = access.ActionDelete
	}
	if err := s.authorizeObject(ctx, action, obj); err != nil {
		return err
	}
	if hard {
		versions, err := s.versionsForHardDelete(ctx, tenant, bucket, key, obj)
		if err != nil {
			return err
		}
		refs := s.collectDeleteRefs(ctx, obj, versions, true)
		return s.hardDeleteObjectWithVersions(ctx, obj, tenant, bucket, key, versions, &refs)
	}
	refs := s.collectDeleteRefs(ctx, obj, []repository.Object{obj}, false)
	return s.softDeleteObjectWithRefs(ctx, obj, tenant, bucket, key, &refs)
}

func (s *FileService) collectDeleteRefs(
	ctx context.Context, obj repository.Object, versions []repository.Object, hard bool,
) deleteRefs {
	refs := deleteRefs{}
	if s.shareLister != nil {
		shares, err := s.shareLister.ListShares(ctx, obj.TenantID, obj.Bucket, obj.Key)
		if err != nil {
			s.logger.Warn("admin delete: list shares failed", "key", obj.Key, "err", err)
		} else {
			for _, share := range shares {
				if share.RevokedAt.IsZero() {
					refs.ShareIDs = append(refs.ShareIDs, share.ID)
				}
			}
		}
	}
	if !hard {
		refs.VersionCount = 1
		refs.ChunkCount = s.chunkCount(ctx, obj.ID, obj.Key)
		return refs
	}
	for _, version := range versions {
		if IsDeleteMarker(version) {
			continue
		}
		refs.VersionCount++
		refs.ChunkCount += s.chunkCount(ctx, version.ID, version.Key)
	}
	return refs
}

func (s *FileService) chunkCount(ctx context.Context, objectID int64, key string) int {
	chunks, err := s.repo.ListChunksForObject(ctx, objectID)
	if err != nil {
		s.logger.Warn("admin delete: list chunks failed", "object_id", objectID, "key", key, "err", err)
		return 0
	}
	return len(chunks)
}
