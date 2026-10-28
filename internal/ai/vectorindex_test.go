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

	obj := env.putObject(t, "k.txt", "text/plain", "hello")
	seedCanonicalQueryRows(t, env, obj, emb, "c1", "c2", "c3", "c4", "c5", "c6", "hello")
	rows, err := env.repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("seeded %d chunks, want 7", len(rows))
	}
	if obj.ID != 1 {
		t.Fatalf("fixture object ID=%d, want original candidate object 1", obj.ID)
	}
	fake := &fakeVectorIndex{hits: []repository.SearchHit{{
		Score: 0.99,
		Chunk: repository.Chunk{ID: 7, ObjectID: 1, TenantID: testTenant,
			Bucket: testBucket, ObjectKey: "k.txt", Seq: 0, Content: "hello", EmbedModel: emb.Name()},
	}}}

	s := NewSearch(env.repo, emb, nil).WithVectorIndex(fake).WithCanonicalResources(env.repo)
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

// seedCanonicalQueryRows preserves the original canned query candidates' Seq=0
// (including repeated sequences), payloads and real auto-assigned IDs. The
// authoritative fixture adapts to the inputs, never the other way around.
func seedCanonicalQueryRows(t *testing.T, env *testEnv, obj repository.Object, emb Embedder, contents ...string) {
	t.Helper()
	vecs, err := emb.Embed(context.Background(), contents)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]repository.Chunk, len(contents))
	for i, content := range contents {
		rows[i] = repository.Chunk{ObjectID: obj.ID, TenantID: obj.TenantID,
			Bucket: obj.Bucket, ObjectKey: obj.Key, Seq: 0, Content: content,
			Embedding: vecs[i], Dim: len(vecs[i]), EmbedModel: emb.Name()}
	}
	if err := env.repo.InsertChunks(context.Background(), rows); err != nil {
		t.Fatal(err)
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
