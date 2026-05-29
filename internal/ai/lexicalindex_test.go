package ai

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type fakeLexical struct {
	called bool
	hits   []repository.SearchHit
}

func (f *fakeLexical) SearchLexical(_ context.Context, _, _, _ string, _ int) ([]repository.SearchHit, error) {
	f.called = true
	return f.hits, nil
}

func TestSearch_UsesInjectedLexicalIndex(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	fake := &fakeLexical{hits: []repository.SearchHit{{
		Score: 0.5,
		Chunk: repository.Chunk{ID: 9, ObjectID: 1, Bucket: testBucket, ObjectKey: "k.txt", Content: "x", EmbedModel: "fts"},
	}}}

	s := NewSearch(env.repo, NewHashEmbedder(64), nil).WithLexicalIndex(fake)
	hits, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "hi", K: 5, Mode: "bm25"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !fake.called {
		t.Fatal("bm25 mode should route through the injected LexicalIndex")
	}
	if len(hits) != 1 || hits[0].ChunkID != 9 {
		t.Fatalf("expected the injected lexical hit (chunk 9), got %+v", hits)
	}
}

func TestOpenPgFTSIndex_EmptyDSN(t *testing.T) {
	if _, err := OpenPgFTSIndex(context.Background(), "", PgFTSOptions{}); err == nil {
		t.Fatal("expected error for empty dsn")
	}
}

var _ LexicalIndex = (*fakeLexical)(nil)
