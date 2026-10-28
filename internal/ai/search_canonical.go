package ai

import (
	"context"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// ErrCanonicalHit marks a candidate that cannot be proven against the current
// authoritative object/chunk rows. It is a fault, not an ordinary ACL denial.
var ErrCanonicalHit = errors.New("canonical search hit rejected")

type canonicalObject struct {
	object repository.Object
	chunks map[int64]repository.Chunk
}

type canonicalResolver struct {
	search  *Search
	tenant  string
	objects map[int64]*canonicalObject
}

type canonicalResult struct {
	hits   []Hit
	chunks map[int64]repository.Chunk
}

func canonicalReject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCanonicalHit, fmt.Sprintf(format, args...))
}

// validateRankedScope runs before hitsFromRanked so the tenant identity carried
// by the retrieval row is not silently discarded during Hit conversion.
func validateRankedScope(req Request, hits []ranked) error {
	for _, hit := range hits {
		if hit.chunkID <= 0 || hit.chunk.ID != hit.chunkID {
			return canonicalReject("invalid chunk identity %d/%d", hit.chunkID, hit.chunk.ID)
		}
		if hit.chunk.ObjectID <= 0 {
			return canonicalReject("invalid object identity %d", hit.chunk.ObjectID)
		}
		if hit.chunk.TenantID != req.Tenant {
			return canonicalReject("cross-tenant chunk %d", hit.chunkID)
		}
		if err := validateHitShape(req, hitsFromRanked([]ranked{hit})[0]); err != nil {
			return err
		}
	}
	return nil
}

func validateHitShape(req Request, hit Hit) error {
	if hit.ChunkID <= 0 || hit.Seq < 0 {
		return canonicalReject("invalid chunk identity %d/%d", hit.ChunkID, hit.Seq)
	}
	if req.Bucket != "" && hit.Bucket != req.Bucket {
		return canonicalReject("hit %d is outside the requested bucket", hit.ChunkID)
	}
	if err := access.ValidateChunkRef(access.ChunkRef{
		TenantID: req.Tenant, Bucket: hit.Bucket, Key: hit.ObjectKey, ObjectID: hit.ObjectID,
	}); err != nil {
		return fmt.Errorf("%w: hit scope: %w", ErrCanonicalHit, err)
	}
	return nil
}

func (s *Search) canonicalizeHits(ctx context.Context, req Request, hits []Hit) (*canonicalResult, error) {
	if err := searchContextError(ctx); err != nil {
		return nil, err
	}
	if nilLike(s.resources) {
		return nil, canonicalReject("canonical resource reader unavailable")
	}
	resolver := &canonicalResolver{search: s, tenant: req.Tenant, objects: map[int64]*canonicalObject{}}
	out := make([]Hit, len(hits))
	chunks := make(map[int64]repository.Chunk, len(hits))
	for i, hit := range hits {
		if err := validateHitShape(req, hit); err != nil {
			return nil, err
		}
		chunk, err := resolver.resolve(ctx, hit)
		if err != nil {
			return nil, err
		}
		if _, duplicate := chunks[hit.ChunkID]; duplicate {
			return nil, canonicalReject("duplicate chunk %d", hit.ChunkID)
		}
		chunks[hit.ChunkID] = chunk
		out[i] = hit
	}
	return &canonicalResult{hits: out, chunks: chunks}, nil
}

func (r *canonicalResolver) resolve(ctx context.Context, hit Hit) (repository.Chunk, error) {
	object, err := r.object(ctx, hit.ObjectID)
	if err != nil {
		return repository.Chunk{}, err
	}
	chunk, ok := object.chunks[hit.ChunkID]
	if !ok {
		return repository.Chunk{}, canonicalReject("chunk %d is not part of object %d", hit.ChunkID, hit.ObjectID)
	}
	if err := matchHitToCanonical(r.tenant, hit, object.object, chunk); err != nil {
		return repository.Chunk{}, err
	}
	return chunk, nil
}

func (r *canonicalResolver) object(ctx context.Context, objectID int64) (*canonicalObject, error) {
	if objectID <= 0 {
		return nil, canonicalReject("invalid object id %d", objectID)
	}
	if cached, ok := r.objects[objectID]; ok {
		return cached, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	obj, err := r.search.resources.GetObjectByID(ctx, objectID)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return nil, fmt.Errorf("%w: object %d lookup: %w", ErrCanonicalHit, objectID, err)
	}
	if err := validateCanonicalObject(r.tenant, objectID, obj); err != nil {
		return nil, err
	}
	if err := r.search.currentCanonicalObject(ctx, r.tenant, obj); err != nil {
		return nil, err
	}
	rows, err := r.search.resources.ListChunksForObject(ctx, objectID)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return nil, fmt.Errorf("%w: object %d chunks lookup: %w", ErrCanonicalHit, objectID, err)
	}
	chunks, err := completeCanonicalChunks(r.tenant, obj, rows)
	if err != nil {
		return nil, err
	}
	if err := r.search.currentCanonicalObject(ctx, r.tenant, obj); err != nil {
		return nil, err
	}
	value := &canonicalObject{object: obj, chunks: chunks}
	r.objects[objectID] = value
	return value, nil
}

func validateCanonicalObject(tenant string, objectID int64, obj repository.Object) error {
	if obj.ID != objectID || obj.ID <= 0 {
		return canonicalReject("object id mismatch %d/%d", objectID, obj.ID)
	}
	if obj.TenantID != tenant {
		return canonicalReject("object %d tenant mismatch", objectID)
	}
	if err := access.ValidateChunkRef(access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}); err != nil {
		return fmt.Errorf("%w: object scope: %w", ErrCanonicalHit, err)
	}
	if obj.DeletedAt != nil || obj.VersionTombstone {
		return canonicalReject("object %d is not current", objectID)
	}
	return nil
}

func completeCanonicalChunks(tenant string, obj repository.Object, rows []repository.Chunk) (map[int64]repository.Chunk, error) {
	chunks := make(map[int64]repository.Chunk, len(rows))
	for _, chunk := range rows {
		if chunk.ID <= 0 || chunk.Seq < 0 || chunk.ObjectID != obj.ID || chunk.TenantID != tenant ||
			chunk.Bucket != obj.Bucket || chunk.ObjectKey != obj.Key {
			return nil, canonicalReject("object %d has incomplete chunk scope", obj.ID)
		}
		if _, duplicate := chunks[chunk.ID]; duplicate {
			return nil, canonicalReject("object %d has duplicate chunk %d", obj.ID, chunk.ID)
		}
		chunks[chunk.ID] = chunk
	}
	return chunks, nil
}

func matchHitToCanonical(tenant string, hit Hit, obj repository.Object, chunk repository.Chunk) error {
	if obj.TenantID != tenant || obj.Bucket != hit.Bucket || obj.Key != hit.ObjectKey {
		return canonicalReject("hit %d object scope mismatch", hit.ChunkID)
	}
	if chunk.ObjectID != hit.ObjectID || chunk.Bucket != hit.Bucket || chunk.ObjectKey != hit.ObjectKey ||
		chunk.Seq != hit.Seq || chunk.Content != hit.Chunk || chunk.EmbedModel != hit.EmbedModel {
		return canonicalReject("hit %d payload mismatch", hit.ChunkID)
	}
	return nil
}

func (s *Search) currentCanonicalObject(ctx context.Context, tenant string, expected repository.Object) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if nilLike(s.repo) {
		return canonicalReject("current object repository unavailable")
	}
	current, err := s.repo.GetObject(ctx, tenant, expected.Bucket, expected.Key)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return fmt.Errorf("%w: current object lookup: %w", ErrCanonicalHit, err)
	}
	if err := validateCanonicalObject(tenant, expected.ID, current); err != nil {
		return err
	}
	if !sameObjectObservation(expected, current) {
		return canonicalReject("object %d is no longer the observed current object", expected.ID)
	}
	return nil
}

func sameObjectObservation(expected, current repository.Object) bool {
	return expected.ID == current.ID && expected.TenantID == current.TenantID &&
		expected.Bucket == current.Bucket && expected.Key == current.Key &&
		expected.VersionID == current.VersionID && sameObjectContent(expected, current)
}

func sameObjectContent(expected, current repository.Object) bool {
	return expected.ETag == current.ETag && expected.Size == current.Size &&
		expected.StorageKey == current.StorageKey && expected.Backend == current.Backend &&
		expected.ContentType == current.ContentType && expected.UpdatedAt.Equal(current.UpdatedAt)
}

// guardReranked accepts only candidates that were present in the canonical
// input snapshot and whose identities/content still match that snapshot. A
// reranker therefore cannot inject an ID or rewrite text into the final set.
func guardReranked(input *canonicalResult, reranked []Hit, tenant string) ([]Hit, error) {
	if input == nil {
		return nil, canonicalReject("missing canonical snapshot")
	}
	seen := make(map[int64]struct{}, len(reranked))
	out := make([]Hit, 0, len(reranked))
	for _, hit := range reranked {
		chunk, ok := input.chunks[hit.ChunkID]
		if !ok {
			return nil, canonicalReject("reranker injected chunk %d", hit.ChunkID)
		}
		if _, duplicate := seen[hit.ChunkID]; duplicate {
			return nil, canonicalReject("reranker duplicated chunk %d", hit.ChunkID)
		}
		seen[hit.ChunkID] = struct{}{}
		if err := matchHitToCanonical(tenant, hit, repository.Object{
			TenantID: tenant, Bucket: hit.Bucket, Key: hit.ObjectKey,
		}, chunk); err != nil {
			return nil, err
		}
		out = append(out, hit)
	}
	return out, nil
}
