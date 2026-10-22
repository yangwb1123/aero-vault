package reconcile

import (
	"context"
	"errors"
	"reflect"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

var errScopedChunkCleanerRequired = errors.New("scoped chunk cleaner required")

// deleteScopedObjectChunks preserves the true-nil AI-off path, but never
// treats an incompatible configured cleaner as optional or falls back to IDs.
func deleteScopedObjectChunks(ctx context.Context, cleaner ChunkCleaner, obj repository.Object) error {
	if cleaner == nil {
		return nil
	}
	scoped, ok := cleaner.(access.ScopedChunkCleaner)
	if !ok || nilScopedChunkCleaner(scoped) {
		return errScopedChunkCleanerRequired
	}
	ref := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	if err := access.ValidateChunkRef(ref); err != nil {
		return err
	}
	return scoped.DeleteScopedObjectChunks(ctx, ref)
}

func nilScopedChunkCleaner(cleaner access.ScopedChunkCleaner) bool {
	if cleaner == nil {
		return true
	}
	value := reflect.ValueOf(cleaner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
