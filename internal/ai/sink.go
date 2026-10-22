package ai

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// ChunkSink is a write-through hook that keeps a secondary index (an external
// vector store, the in-memory BM25) in step with the repository's chunk rows.
// Sinks receive the canonical post-insert rows — real chunk IDs and embeddings
// — never the pre-insert slice. UpsertObjectChunks means "replace everything
// you hold for this object with exactly these chunks". Implementations must be
// safe for concurrent use.
//
// The ID-only methods are retained for source compatibility. Indexer production
// writes require ScopedChunkSink and never fall back to these methods.
type ChunkSink interface {
	UpsertObjectChunks(ctx context.Context, objectID int64, chunks []repository.Chunk) error
	DeleteObjectChunks(ctx context.Context, objectID int64) error
}

// ScopedChunkSink is the production write seam. The complete ChunkRef is the
// only object identity a sink may use; it is not an authorization grant.
type ScopedChunkSink interface {
	UpsertScopedObjectChunks(ctx context.Context, ref access.ChunkRef, chunks []repository.Chunk) error
	DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error
}

// registerChunkSink latches the first invalid sink registration. A true nil or
// typed-nil value, and a legacy-only ChunkSink, are configuration faults rather
// than requests to use an ID-only fallback.
func (ix *Indexer) registerChunkSink(s ChunkSink) {
	if nilLike(s) {
		ix.latchSinkFault(errors.New("chunk sink: nil or typed-nil registration"))
		return
	}
	scoped, ok := s.(ScopedChunkSink)
	if !ok || nilLike(scoped) {
		ix.latchSinkFault(fmt.Errorf("chunk sink %T: scoped chunk sink capability required", s))
		return
	}
	ix.scoped = append(ix.scoped, scoped)
}

func (ix *Indexer) latchSinkFault(err error) {
	if ix.sinkFault == nil {
		ix.sinkFault = err
	}
}

// ValidateChunkSinks rejects a latched bad registration. Zero registered sinks
// is valid: the indexer simply runs without secondary indexes.
func (ix *Indexer) ValidateChunkSinks() error {
	if ix.sinkFault != nil {
		return fmt.Errorf("chunk sink configuration: %w", ix.sinkFault)
	}
	return nil
}

func nilLike(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
