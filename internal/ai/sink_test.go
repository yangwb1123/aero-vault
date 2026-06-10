package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type fakeSink struct {
	upsertCalls  int
	lastObjectID int64
	lastChunks   []repository.Chunk
	upsertErr    error

	deleteCalls int
	deletedIDs  []int64
	deleteErr   error
}

func (s *fakeSink) UpsertObjectChunks(_ context.Context, objectID int64, chunks []repository.Chunk) error {
	s.upsertCalls++
	s.lastObjectID = objectID
	s.lastChunks = chunks
	return s.upsertErr
}

func (s *fakeSink) DeleteObjectChunks(_ context.Context, objectID int64) error {
	s.deleteCalls++
	s.deletedIDs = append(s.deletedIDs, objectID)
	return s.deleteErr
}

// countingRepo wraps the test repository to observe the canonical re-read the
// indexer performs for sinks.
type countingRepo struct {
	repository.Repository
	listChunksCalls int
}

func (r *countingRepo) ListChunksForObject(ctx context.Context, objectID int64) ([]repository.Chunk, error) {
	r.listChunksCalls++
	return r.Repository.ListChunksForObject(ctx, objectID)
}

func TestIndexer_SinkReceivesCanonicalChunks(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "doc.txt", "text/plain", "alpha beta gamma")

	sink := &fakeSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(sink)

	if err := ix.IndexObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("IndexObjectByID: %v", err)
	}
	if sink.upsertCalls != 1 {
		t.Fatalf("expected 1 upsert, got %d", sink.upsertCalls)
	}
	if sink.lastObjectID != obj.ID {
		t.Fatalf("upsert for object %d, want %d", sink.lastObjectID, obj.ID)
	}
	if len(sink.lastChunks) == 0 {
		t.Fatal("expected chunks delivered to sink")
	}
	for i, c := range sink.lastChunks {
		if c.ID == 0 {
			t.Fatalf("chunk %d has zero ID; sinks must see post-insert rows", i)
		}
		if len(c.Embedding) == 0 {
			t.Fatalf("chunk %d has no embedding", i)
		}
	}
}

func TestIndexer_AllSinksReceiveUpsert(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "doc.txt", "text/plain", "alpha beta gamma")

	s1, s2 := &fakeSink{}, &fakeSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(s1).
		WithChunkSink(s2)

	if err := ix.IndexObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("IndexObjectByID: %v", err)
	}
	for i, s := range []*fakeSink{s1, s2} {
		if s.upsertCalls != 1 {
			t.Fatalf("sink %d: expected 1 upsert, got %d", i, s.upsertCalls)
		}
		if s.lastObjectID != obj.ID {
			t.Fatalf("sink %d: upsert for object %d, want %d", i, s.lastObjectID, obj.ID)
		}
	}
}

func TestIndexer_SinkUpsertErrorPropagates(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "doc.txt", "text/plain", "alpha beta gamma")

	sinkErr := errors.New("vector store down")
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(&fakeSink{upsertErr: sinkErr})

	err := ix.IndexObjectByID(ctx, obj.ID)
	if !errors.Is(err, sinkErr) {
		t.Fatalf("expected sink error to propagate, got %v", err)
	}
}

func TestIndexer_DeleteObjectChunksPropagatesToSinks(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "doc.txt", "text/plain", "alpha beta gamma")

	s1, s2 := &fakeSink{}, &fakeSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(s1).
		WithChunkSink(s2)

	if err := ix.DeleteObjectChunks(ctx, obj.ID); err != nil {
		t.Fatalf("DeleteObjectChunks: %v", err)
	}
	for i, s := range []*fakeSink{s1, s2} {
		if s.deleteCalls != 1 || len(s.deletedIDs) != 1 || s.deletedIDs[0] != obj.ID {
			t.Fatalf("sink %d: expected one delete for object %d, got calls=%d ids=%v",
				i, obj.ID, s.deleteCalls, s.deletedIDs)
		}
	}

	sinkErr := errors.New("vector store down")
	ixErr := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(&fakeSink{deleteErr: sinkErr})
	if err := ixErr.DeleteObjectChunks(ctx, obj.ID); !errors.Is(err, sinkErr) {
		t.Fatalf("expected sink delete error to propagate, got %v", err)
	}
}

func TestIndexer_NoSinksSkipsCanonicalReread(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "doc.txt", "text/plain", "alpha beta gamma")

	crepo := &countingRepo{Repository: env.repo}
	ix := NewIndexer(crepo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil)

	if err := ix.IndexObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("IndexObjectByID: %v", err)
	}
	if crepo.listChunksCalls != 0 {
		t.Fatalf("expected no canonical re-read without sinks, got %d calls", crepo.listChunksCalls)
	}
	chunks, err := env.repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks inserted")
	}
}
