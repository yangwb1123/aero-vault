package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

var _ ScopedChunkSink = (*QdrantIndex)(nil)

// DeleteScopedObjectChunks removes only points matching the complete four-field
// ref. Legacy object-id-only points do not match and are deliberately preserved.
func (q *QdrantIndex) DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return fmt.Errorf("qdrant delete: %w", err)
	}
	return q.deleteScopedObjectChunks(ctx, ref)
}

func (q *QdrantIndex) deleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error {
	body := qdrantDeleteReq{Filter: scopedChunkFilter(ref)}
	return q.do(ctx, http.MethodPost, "/points/delete", "wait=true", body, nil)
}

// UpsertScopedObjectChunks validates the ref, every row and the complete JSON
// request before the destructive scoped delete. Only after that delete succeeds
// does it PUT the new points with wait=true.
func (q *QdrantIndex) UpsertScopedObjectChunks(ctx context.Context, ref access.ChunkRef, chunks []repository.Chunk) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return fmt.Errorf("qdrant upsert: %w", err)
	}
	points, err := scopedQdrantPoints(ref, chunks)
	if err != nil {
		return err
	}
	req := qdrantUpsertReq{Points: points}
	if len(points) > 0 {
		if _, err := json.Marshal(req); err != nil {
			return fmt.Errorf("qdrant upsert: encode request: %w", err)
		}
	}
	if err := q.deleteScopedObjectChunks(ctx, ref); err != nil {
		return err
	}
	if len(points) == 0 {
		return nil
	}
	return q.do(ctx, http.MethodPut, "/points", "wait=true", req, nil)
}

func scopedChunkFilter(ref access.ChunkRef) qdrantFilter {
	return qdrantFilter{Must: []qdrantMatch{
		{Key: "tenant_id", Match: qdrantMatchVal{Value: ref.TenantID}},
		{Key: "bucket", Match: qdrantMatchVal{Value: ref.Bucket}},
		{Key: "object_id", Match: qdrantMatchVal{Value: ref.ObjectID}},
		{Key: "object_key", Match: qdrantMatchVal{Value: ref.Key}},
	}}
}

func scopedQdrantPoints(ref access.ChunkRef, chunks []repository.Chunk) ([]qdrantPoint, error) {
	points := make([]qdrantPoint, 0, len(chunks))
	seen := make(map[int64]struct{}, len(chunks))
	batchDim := 0
	for _, c := range chunks {
		if err := validateScopedChunkRow(ref, c); err != nil {
			return nil, err
		}
		if _, ok := seen[c.ID]; ok {
			return nil, fmt.Errorf("qdrant upsert: duplicate chunk ID %d", c.ID)
		}
		seen[c.ID] = struct{}{}
		if len(c.Embedding) == 0 {
			continue
		}
		if batchDim == 0 {
			batchDim = c.Dim
		} else if c.Dim != batchDim {
			return nil, fmt.Errorf("qdrant upsert: inconsistent vector dimensions %d and %d", batchDim, c.Dim)
		}
		points = append(points, qdrantPoint{
			ID:     c.ID,
			Vector: c.Embedding,
			Payload: chunkPayload{
				TenantID: c.TenantID, Bucket: c.Bucket, ObjectID: c.ObjectID,
				ObjectKey: c.ObjectKey, Seq: c.Seq, Content: c.Content,
				Dim: c.Dim, EmbedModel: c.EmbedModel,
			},
		})
	}
	return points, nil
}

func validateScopedChunkRow(ref access.ChunkRef, c repository.Chunk) error {
	if c.ID <= 0 {
		return fmt.Errorf("qdrant upsert: chunk ID must be positive")
	}
	row := access.ChunkRef{TenantID: c.TenantID, Bucket: c.Bucket, Key: c.ObjectKey, ObjectID: c.ObjectID}
	if row != ref {
		return refConflict(row, "qdrant chunk row")
	}
	if len(c.Embedding) == 0 {
		return nil
	}
	if c.Dim != len(c.Embedding) || c.Dim <= 0 {
		return fmt.Errorf("qdrant upsert: chunk %d dim %d does not match vector length %d", c.ID, c.Dim, len(c.Embedding))
	}
	for _, v := range c.Embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("qdrant upsert: chunk %d vector contains NaN or Inf", c.ID)
		}
	}
	return nil
}
