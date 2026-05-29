package ai

import (
	"context"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// VectorIndex performs nearest-neighbour search over a tenant's chunk
// embeddings and returns the top-`limit` hits. It is the seam that lets the
// retrieval backend evolve without touching Search:
//
//   - repoVectorIndex (default) is the brute-force scan via the repository —
//     zero-dependency, correct, and fine up to ~100K chunks/tenant.
//   - a pgvector adapter (HNSW/IVFFlat) or an external store (Qdrant/Milvus)
//     can implement the same contract for large corpora; only the wiring in
//     main() changes, not Search.
//
// Implementations must enforce embedding-model identity themselves where
// relevant (the repository scan already filters by dimension).
type VectorIndex interface {
	SearchVectors(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]repository.SearchHit, error)
}

// repoVectorIndex is the default brute-force implementation backed by
// repository.SearchChunks.
type repoVectorIndex struct{ repo repository.Repository }

func (r repoVectorIndex) SearchVectors(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]repository.SearchHit, error) {
	return r.repo.SearchChunks(ctx, tenant, bucket, query, limit)
}
