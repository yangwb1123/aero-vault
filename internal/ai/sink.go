package ai

import (
	"context"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// ChunkSink is a write-through hook that keeps a secondary index (an external
// vector store, the in-memory BM25) in step with the repository's chunk rows.
// Sinks receive the canonical post-insert rows — real chunk IDs and embeddings
// — never the pre-insert slice. UpsertObjectChunks means "replace everything
// you hold for this object with exactly these chunks". Implementations must be
// safe for concurrent use.
type ChunkSink interface {
	UpsertObjectChunks(ctx context.Context, objectID int64, chunks []repository.Chunk) error
	DeleteObjectChunks(ctx context.Context, objectID int64) error
}
