package s3compat

import (
	"context"

	"github.com/aero-vault/aero-vault/internal/service"
)

// deleteS3Object implements S3 version-aware delete semantics and returns the
// version identifier plus whether the deleted entity is a delete marker.
func (h *Handler) deleteS3Object(
	ctx context.Context, tenant, bucket, key, versionID string,
) (string, bool, error) {
	if versionID != "" {
		obj, err := h.svc.GetObjectRetention(ctx, tenant, bucket, key, versionID)
		if err != nil {
			return versionID, false, err
		}
		if err := h.svc.DeleteVersion(ctx, tenant, bucket, key, versionID); err != nil {
			return versionID, service.IsDeleteMarker(obj), err
		}
		return versionID, service.IsDeleteMarker(obj), nil
	}
	cfg, err := h.svc.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return "", false, err
	}
	if cfg.Versioning {
		marker, err := h.svc.CreateDeleteMarker(ctx, tenant, bucket, key)
		return marker.VersionID, true, err
	}
	return "", false, h.svc.Delete(ctx, tenant, bucket, key, true)
}
