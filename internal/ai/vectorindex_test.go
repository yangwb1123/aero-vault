package ai

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// fakeVectorIndex records that it was consulted and returns canned hits, so we
// can prove Search routes vector retrieval through the VectorIndex seam.
type fakeVectorIndex struct {
	called bool
	hits   []repository.SearchHit
}

func (f *fakeVectorIndex) SearchVectors(_ context.Context, _, _ string, _ []float32, _ int) ([]repository.SearchHit, error) {
	f.called = true
	return f.hits, nil
}

func TestSearch_UsesInjectedVectorIndex(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)

	fake := &fakeVectorIndex{hits: []repository.SearchHit{{
		Score: 0.99,
		Chunk: repository.Chunk{ID: 7, ObjectID: 1, Bucket: testBucket, ObjectKey: "k.txt", Seq: 0, Content: "hello", EmbedModel: emb.Name()},
	}}}

	s := NewSearch(env.repo, emb, nil).WithVectorIndex(fake)
	hits, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "hi", K: 5, Mode: "vector"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !fake.called {
		t.Fatal("Search must route vector retrieval through the injected VectorIndex")
	}
	if len(hits) != 1 || hits[0].ChunkID != 7 {
		t.Fatalf("expected the injected hit (chunk 7), got %+v", hits)
	}
}

// TestSearch_DefaultVectorIndexIsBruteForce confirms NewSearch wires the
// brute-force repository index by default (so existing behavior is unchanged).
func TestSearch_DefaultVectorIndexIsBruteForce(t *testing.T) {
	env := newTestEnv(t)
	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	if _, ok := s.vindex.(repoVectorIndex); !ok {
		t.Fatalf("default VectorIndex = %T, want repoVectorIndex", s.vindex)
	}
}
