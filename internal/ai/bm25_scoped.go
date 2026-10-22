package ai

import (
	"context"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

var _ ScopedChunkSink = (*BM25)(nil)

// UpsertScopedObjectChunks replaces exactly one ref's documents. All incoming
// rows, the existing documents being replaced and chunk-ID conflicts are
// validated under one write lock before any map, DF or counter mutation.
func (b *BM25) UpsertScopedObjectChunks(_ context.Context, ref access.ChunkRef, chunks []repository.Chunk) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return fmt.Errorf("bm25 upsert: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.validateScopedChunksLocked(ref, chunks); err != nil {
		return err
	}
	b.removeObjectLocked(ref.ObjectID)
	for _, c := range chunks {
		b.insertDocLocked(c)
	}
	b.recomputeAvgLenLocked()
	b.dirty = true
	return nil
}

// DeleteScopedObjectChunks removes exactly one ref's documents. Mixed existing
// scope for the same object ID is refused without changing any state.
func (b *BM25) DeleteScopedObjectChunks(_ context.Context, ref access.ChunkRef) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return fmt.Errorf("bm25 delete: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.validateExistingObjectLocked(ref); err != nil {
		return err
	}
	b.removeObjectLocked(ref.ObjectID)
	b.recomputeAvgLenLocked()
	b.dirty = true
	return nil
}

func (b *BM25) validateScopedChunksLocked(ref access.ChunkRef, chunks []repository.Chunk) error {
	incoming := make(map[int64]struct{}, len(chunks))
	for _, c := range chunks {
		if c.ID <= 0 {
			return fmt.Errorf("bm25 upsert: chunk ID must be positive")
		}
		row := access.ChunkRef{TenantID: c.TenantID, Bucket: c.Bucket, Key: c.ObjectKey, ObjectID: c.ObjectID}
		if row != ref {
			return refConflict(row, "bm25 chunk row")
		}
		if _, ok := incoming[c.ID]; ok {
			return fmt.Errorf("bm25 upsert: duplicate chunk ID %d", c.ID)
		}
		incoming[c.ID] = struct{}{}
	}
	if err := b.validateExistingObjectLocked(ref); err != nil {
		return err
	}
	old := make(map[int64]struct{}, len(b.objDocs[ref.ObjectID]))
	for _, id := range b.objDocs[ref.ObjectID] {
		old[id] = struct{}{}
	}
	for id := range incoming {
		if _, exists := b.docs[id]; exists {
			if _, replaced := old[id]; !replaced {
				return fmt.Errorf("bm25 upsert: chunk ID %d already belongs to another object", id)
			}
		}
	}
	return nil
}

func (b *BM25) validateExistingObjectLocked(ref access.ChunkRef) error {
	for _, id := range b.objDocs[ref.ObjectID] {
		doc, ok := b.docs[id]
		if !ok {
			return fmt.Errorf("bm25: object %d chunk index references missing doc %d", ref.ObjectID, id)
		}
		got := access.ChunkRef{TenantID: doc.tenant, Bucket: doc.bucket, Key: doc.objectKey, ObjectID: doc.objectID}
		if got != ref {
			return refConflict(got, fmt.Sprintf("bm25 object %d", ref.ObjectID))
		}
	}
	return nil
}

func collectTenantChunks(ctx context.Context, repo repository.Repository, tenant string) ([]repository.Chunk, error) {
	if tenant == "" {
		return nil, fmt.Errorf("bm25 warmup: empty tenant")
	}
	buckets, err := repo.ListBuckets(ctx, tenant)
	if err != nil {
		return nil, err
	}
	const pageSize = 500
	var all []repository.Chunk
	seen := make(map[int64]struct{})
	for _, bucket := range buckets {
		chunks, err := collectBucketChunks(ctx, repo, tenant, bucket, pageSize, seen)
		if err != nil {
			return nil, err
		}
		all = append(all, chunks...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return all, nil
}

func collectBucketChunks(ctx context.Context, repo repository.Repository, tenant, bucket string, pageSize int, seen map[int64]struct{}) ([]repository.Chunk, error) {
	if bucket == "" {
		return nil, fmt.Errorf("bm25 warmup: empty bucket for tenant %q", tenant)
	}
	var all []repository.Chunk
	marker := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := repo.ListObjects(ctx, tenant, bucket, "", marker, pageSize)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Objects {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			chunks, err := collectObjectChunks(ctx, repo, tenant, bucket, o, seen)
			if err != nil {
				return nil, err
			}
			all = append(all, chunks...)
		}
		if !page.HasMore {
			return all, nil
		}
		if page.NextMarker == "" || page.NextMarker == marker {
			return nil, fmt.Errorf("bm25 warmup: pagination did not advance for tenant %q bucket %q", tenant, bucket)
		}
		marker = page.NextMarker
	}
}

func collectObjectChunks(ctx context.Context, repo repository.Repository, tenant, bucket string, o repository.Object, seen map[int64]struct{}) ([]repository.Chunk, error) {
	if o.ID <= 0 || o.TenantID != tenant || o.Bucket != bucket || o.Key == "" {
		return nil, fmt.Errorf("bm25 warmup: incomplete object fact id=%d tenant=%q bucket=%q key=%q", o.ID, o.TenantID, o.Bucket, o.Key)
	}
	ref := access.ChunkRef{TenantID: tenant, Bucket: bucket, Key: o.Key, ObjectID: o.ID}
	chunks, err := repo.ListChunksForObject(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range chunks {
		if c.ID <= 0 {
			return nil, fmt.Errorf("bm25 warmup: object %d has nonpositive chunk ID %d", o.ID, c.ID)
		}
		if _, ok := seen[c.ID]; ok {
			return nil, fmt.Errorf("bm25 warmup: duplicate chunk ID %d", c.ID)
		}
		seen[c.ID] = struct{}{}
		row := access.ChunkRef{TenantID: c.TenantID, Bucket: c.Bucket, Key: c.ObjectKey, ObjectID: c.ObjectID}
		if row != ref {
			return nil, refConflict(row, fmt.Sprintf("bm25 warmup object %d", o.ID))
		}
	}
	return chunks, nil
}
