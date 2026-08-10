package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// errKeyProtected is returned by hardDeleteKey/hardDeleteVersion when any
// version of the key is under legal hold or an active WORM lock. It is a
// package-private sentinel so callers can distinguish "skipped: protected"
// (zero side effects) from genuine failures without mis-counting a skipped
// key as deleted.
var errKeyProtected = errors.New("reconcile: key protected by legal hold or WORM lock")

func hardDeleteKey(
	ctx context.Context,
	repo repository.Repository,
	store storage.Storage,
	cleaner ChunkCleaner,
	obj repository.Object,
	logger *slog.Logger,
) error {
	versions, err := repo.ListObjectVersions(ctx, obj.TenantID, obj.Bucket, obj.Key)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		versions = []repository.Object{obj}
	}
	// Gate before any destructive action (FR-1.1): a hold or WORM lock placed
	// after the caller's pre-check is caught here with zero side effects — no
	// chunk cleanup, no blob delete, no row delete.
	for _, version := range versions {
		protected, err := objectDeletionProtected(ctx, repo, version)
		if err != nil {
			return err
		}
		if protected {
			return errKeyProtected
		}
	}
	for _, version := range versions {
		// Per-version re-check (FR-1.2) narrows the residual TOCTOU window to
		// the width of a single store.Delete call; on hit, abort immediately.
		protected, err := objectDeletionProtected(ctx, repo, version)
		if err != nil {
			return err
		}
		if protected {
			return errKeyProtected
		}
		cleanObjectChunks(ctx, cleaner, version, logger)
		if isDeleteMarker(version) {
			continue
		}
		if err := store.Delete(ctx, version.StorageKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("delete storage key %q: %w", version.StorageKey, err)
		}
	}
	if err := repo.HardDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err != nil {
		return err
	}
	bytes, objects := deletionUsage(versions)
	adjustUsage(ctx, repo, obj.TenantID, -bytes, -objects, logger)
	return nil
}

func softDeleteKey(
	ctx context.Context,
	repo repository.Repository,
	cleaner ChunkCleaner,
	obj repository.Object,
	logger *slog.Logger,
) error {
	cleanObjectChunks(ctx, cleaner, obj, logger)
	if err := repo.SoftDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err != nil {
		return err
	}
	adjustUsage(ctx, repo, obj.TenantID, -obj.Size, -1, logger)
	return nil
}

func hardDeleteVersion(
	ctx context.Context,
	repo repository.Repository,
	store storage.Storage,
	cleaner ChunkCleaner,
	obj repository.Object,
	logger *slog.Logger,
) error {
	// Re-check before any destructive action (FR-1.4). WORM is covered here
	// because objectDeletionProtected checks LockedUntil in addition to legal
	// holds — the repository-level gate never checks locked_until.
	protected, err := objectDeletionProtected(ctx, repo, obj)
	if err != nil {
		return err
	}
	if protected {
		return errKeyProtected
	}
	cleanObjectChunks(ctx, cleaner, obj, logger)
	if !isDeleteMarker(obj) {
		if err := store.Delete(ctx, obj.StorageKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
	}
	if err := repo.HardDeleteObjectByID(ctx, obj.ID); err != nil {
		return err
	}
	bytes, objects := deletionUsage([]repository.Object{obj})
	adjustUsage(ctx, repo, obj.TenantID, -bytes, -objects, logger)
	return nil
}

func cleanObjectChunks(
	ctx context.Context, cleaner ChunkCleaner, obj repository.Object, logger *slog.Logger,
) {
	if cleaner == nil || isDeleteMarker(obj) {
		return
	}
	if err := cleaner.DeleteObjectChunks(ctx, obj.ID); err != nil {
		logger.Warn("chunk cleanup during reconcile delete failed", "id", obj.ID, "key", obj.Key, "err", err)
	}
}

func deletionUsage(objects []repository.Object) (bytes, count int64) {
	for _, obj := range objects {
		if obj.DeletedAt == nil || obj.VersionTombstone {
			bytes += obj.Size
			count++
		}
	}
	return bytes, count
}

func adjustUsage(
	ctx context.Context,
	repo repository.Repository,
	tenant string,
	bytes, objects int64,
	logger *slog.Logger,
) {
	if bytes == 0 && objects == 0 {
		return
	}
	if _, err := repo.AddTenantUsage(ctx, tenant, bytes, objects); err != nil {
		logger.Warn("reconcile quota adjustment failed", "tenant", tenant, "err", err)
	}
}

func isDeleteMarker(obj repository.Object) bool {
	return obj.Metadata["_aero_delete_marker"] == "true"
}
