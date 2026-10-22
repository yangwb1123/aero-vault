package ai

import (
	"context"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

var _ access.ScopedChunkCleaner = (*Indexer)(nil)

// DeleteScopedObjectChunks is the FileService/reconcile cleaner seam. The ref
// comes from an exact loaded object/version or a trusted persisted payload; it
// is validated here but is not itself an authorization grant.
func (ix *Indexer) DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return fmt.Errorf("delete scoped chunks: %w", err)
	}
	return ix.deleteRefChunks(ctx, ref)
}

// DeleteObjectChunks is the legacy ID-only entry point. It resolves an exact
// ref before any cleanup and never calls an ID-only sink method.
func (ix *Indexer) DeleteObjectChunks(ctx context.Context, objectID int64) error {
	if err := ix.ValidateChunkSinks(); err != nil {
		return err
	}
	ref, err := ix.resolveObjectRef(ctx, objectID, "")
	if err != nil {
		return err
	}
	return ix.deleteRefChunks(ctx, ref)
}

func (ix *Indexer) resolveObjectRef(ctx context.Context, objectID int64, tenant string) (access.ChunkRef, error) {
	if objectID <= 0 {
		return access.ChunkRef{}, fmt.Errorf("chunk ref: object ID must be positive")
	}
	obj, err := ix.repo.GetObjectByID(ctx, objectID)
	if err == nil {
		ref, refErr := refFromObject(obj)
		if refErr != nil {
			return access.ChunkRef{}, refErr
		}
		if tenant != "" && ref.TenantID != tenant {
			return access.ChunkRef{}, refConflict(ref, "request tenant")
		}
		return ref, nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return access.ChunkRef{}, fmt.Errorf("resolve object %d: %w", objectID, err)
	}
	chunks, err := ix.repo.ListChunksForObject(ctx, objectID)
	if err != nil {
		return access.ChunkRef{}, fmt.Errorf("resolve chunks %d: %w", objectID, err)
	}
	ref, err := refFromChunks(objectID, chunks)
	if err != nil {
		return access.ChunkRef{}, err
	}
	if tenant != "" && ref.TenantID != tenant {
		return access.ChunkRef{}, refConflict(ref, "request tenant")
	}
	return ref, nil
}

func (ix *Indexer) cleanupVanishedObject(ctx context.Context, objectID int64, tenant string, expected *access.ChunkRef) error {
	if expected != nil && expected.ObjectID != objectID {
		return refConflict(*expected, fmt.Sprintf("object %d", objectID))
	}
	chunks, err := ix.repo.ListChunksForObject(ctx, objectID)
	if err != nil {
		return fmt.Errorf("list vanished object chunks %d: %w", objectID, err)
	}
	var ref access.ChunkRef
	if expected != nil {
		ref = *expected
		if len(chunks) > 0 {
			if err := validateChunksForRef(ref, chunks); err != nil {
				return err
			}
		}
	} else {
		if len(chunks) == 0 {
			if len(ix.scoped) > 0 {
				return fmt.Errorf("chunk cleanup for object %d unresolved: object and chunk evidence are gone", objectID)
			}
			return nil
		}
		ref, err = refFromChunks(objectID, chunks)
		if err != nil {
			return err
		}
	}
	if tenant != "" && ref.TenantID != tenant {
		return refConflict(ref, "job tenant")
	}
	return ix.deleteRefChunks(ctx, ref)
}

func (ix *Indexer) prepareCurrentObject(ctx context.Context, obj repository.Object) (bool, error) {
	ref, err := refFromObject(obj)
	if err != nil {
		return false, err
	}
	current, err := ix.repo.GetObject(ctx, obj.TenantID, obj.Bucket, obj.Key)
	if errors.Is(err, repository.ErrNotFound) {
		return false, ix.deleteRefChunks(ctx, ref)
	}
	if err != nil {
		return false, err
	}
	if current.ID != obj.ID {
		return false, ix.deleteRefChunks(ctx, ref)
	}
	versions, err := ix.repo.ListObjectVersions(ctx, obj.TenantID, obj.Bucket, obj.Key)
	if err != nil {
		return false, err
	}
	for _, version := range versions {
		if version.ID == current.ID {
			continue
		}
		versionRef, refErr := refFromObject(version)
		if refErr != nil {
			return false, refErr
		}
		if err := ix.deleteRefChunks(ctx, versionRef); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (ix *Indexer) pushToSinks(ctx context.Context, ref access.ChunkRef, inserted []repository.Chunk) error {
	if len(ix.scoped) == 0 {
		return nil
	}
	rows, err := ix.repo.ListChunksForObject(ctx, ref.ObjectID)
	if err != nil {
		return fmt.Errorf("list chunks for sinks %d: %w", ref.ObjectID, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("canonical chunk reread for object %d is empty after inserting %d rows", ref.ObjectID, len(inserted))
	}
	if err := validateChunksForRef(ref, rows); err != nil {
		return err
	}
	for _, sink := range ix.scoped {
		if err := sink.UpsertScopedObjectChunks(ctx, ref, rows); err != nil {
			return fmt.Errorf("sink upsert chunks %d: %w", ref.ObjectID, err)
		}
	}
	return nil
}

func (ix *Indexer) deleteRefChunks(ctx context.Context, ref access.ChunkRef) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	if err := ix.ValidateChunkSinks(); err != nil {
		return err
	}
	if err := ix.validateObservedObjectRef(ctx, ref); err != nil {
		return err
	}
	if len(ix.scoped) > 0 {
		if err := ix.validateObservedChunks(ctx, ref); err != nil {
			return err
		}
	}
	if err := ix.repo.DeleteChunksForObject(ctx, ref.ObjectID); err != nil {
		return err
	}
	for _, sink := range ix.scoped {
		if err := sink.DeleteScopedObjectChunks(ctx, ref); err != nil {
			return fmt.Errorf("sink delete chunks %d: %w", ref.ObjectID, err)
		}
	}
	return nil
}

func (ix *Indexer) validateObservedObjectRef(ctx context.Context, ref access.ChunkRef) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return err
	}
	obj, err := ix.repo.GetObjectByID(ctx, ref.ObjectID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("observe object %d: %w", ref.ObjectID, err)
	}
	observed, err := refFromObject(obj)
	if err != nil {
		return err
	}
	if observed != ref {
		return refConflict(observed, "persisted ref")
	}
	return nil
}

func (ix *Indexer) validateObservedChunks(ctx context.Context, ref access.ChunkRef) error {
	chunks, err := ix.repo.ListChunksForObject(ctx, ref.ObjectID)
	if err != nil {
		return fmt.Errorf("observe chunks %d: %w", ref.ObjectID, err)
	}
	if len(chunks) == 0 {
		return nil
	}
	return validateChunksForRef(ref, chunks)
}

func refFromObject(obj repository.Object) (access.ChunkRef, error) {
	ref := access.ChunkRef{
		TenantID: obj.TenantID,
		Bucket:   obj.Bucket,
		Key:      obj.Key,
		ObjectID: obj.ID,
	}
	if err := access.ValidateChunkRef(ref); err != nil {
		return access.ChunkRef{}, fmt.Errorf("object %d chunk ref: %w", obj.ID, err)
	}
	return ref, nil
}

func refFromChunks(objectID int64, chunks []repository.Chunk) (access.ChunkRef, error) {
	if len(chunks) == 0 {
		return access.ChunkRef{}, fmt.Errorf("chunk ref for object %d unresolved: no object row or chunk evidence", objectID)
	}
	ref := access.ChunkRef{
		TenantID: chunks[0].TenantID,
		Bucket:   chunks[0].Bucket,
		Key:      chunks[0].ObjectKey,
		ObjectID: objectID,
	}
	if err := validateChunksForRef(ref, chunks); err != nil {
		return access.ChunkRef{}, err
	}
	return ref, nil
}

func validateChunksForRef(ref access.ChunkRef, chunks []repository.Chunk) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return fmt.Errorf("chunk rows: %w", err)
	}
	seen := make(map[int64]struct{}, len(chunks))
	for _, c := range chunks {
		if c.ID <= 0 {
			return fmt.Errorf("chunk row for object %d has nonpositive ID %d", ref.ObjectID, c.ID)
		}
		if _, ok := seen[c.ID]; ok {
			return fmt.Errorf("chunk row ID %d is duplicated for object %d", c.ID, ref.ObjectID)
		}
		seen[c.ID] = struct{}{}
		row := access.ChunkRef{TenantID: c.TenantID, Bucket: c.Bucket, Key: c.ObjectKey, ObjectID: c.ObjectID}
		if row != ref {
			return refConflict(row, "canonical chunk rows")
		}
	}
	return nil
}

func refConflict(got access.ChunkRef, source string) error {
	return fmt.Errorf("chunk scope conflict: %s has tenant=%q bucket=%q key=%q object_id=%d",
		source, got.TenantID, got.Bucket, got.Key, got.ObjectID)
}
