package reconcile

import (
	"context"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func objectDeletionProtected(ctx context.Context, repo repository.Repository, obj repository.Object) (bool, error) {
	if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
		return true, nil
	}
	if obj.Metadata["_aero_legal_hold"] == "ON" {
		return true, nil
	}
	return repo.ObjectHasLegalHold(ctx, obj.ID)
}

func objectKeyDeletionProtected(ctx context.Context, repo repository.Repository, obj repository.Object) (bool, error) {
	versions, err := repo.ListObjectVersions(ctx, obj.TenantID, obj.Bucket, obj.Key)
	if err != nil {
		return false, err
	}
	if len(versions) == 0 {
		return objectDeletionProtected(ctx, repo, obj)
	}
	for _, version := range versions {
		protected, err := objectDeletionProtected(ctx, repo, version)
		if err != nil || protected {
			return protected, err
		}
	}
	return false, nil
}
