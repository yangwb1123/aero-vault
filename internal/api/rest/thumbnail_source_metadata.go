package rest

import (
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// Keep the opened source's scalar metadata together when a flight shares its
// result. Do not retain the reader, mutable object metadata, or leader context.
func thumbnailOpenedSource(source service.ThumbnailSource, storeBlocked bool) thumbnail.OpenedSource {
	obj := source.Object
	return thumbnail.OpenedSource{
		Identity:          thumbnailSourceIdentity(obj),
		ETag:              obj.ETag,
		Bound:             source.Bound,
		CacheStoreBlocked: storeBlocked,
		UpdatedAt:         obj.UpdatedAt,
		ObjectID:          obj.ID,
		Backend:           obj.Backend,
		Size:              obj.Size,
		ContentType:       obj.ContentType,
	}
}

// Resident hits use the authorized Stat that selected their cache key. A
// coalesced result instead names its opened source, never a mixed A/B object.
// EmitAccessed still uses the caller's context for request attribution.
func thumbnailAccessObject(stat repository.Object, opened *thumbnail.OpenedSource) repository.Object {
	if opened == nil {
		return stat
	}
	return repository.Object{
		ID:          opened.ObjectID,
		TenantID:    opened.Identity.TenantID,
		Bucket:      opened.Identity.Bucket,
		Key:         opened.Identity.Key,
		VersionID:   opened.Identity.VersionID,
		Backend:     opened.Backend,
		Size:        opened.Size,
		ETag:        opened.ETag,
		ContentType: opened.ContentType,
		UpdatedAt:   opened.UpdatedAt,
	}
}
