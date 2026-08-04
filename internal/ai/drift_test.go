package ai

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestSearch_FiltersEmbeddingModelDrift verifies the drift guard: a chunk
// embedded by a different model (same dimension) is excluded from vector
// results, while the query model's own chunks are returned.
func TestSearch_FiltersEmbeddingModelDrift(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64) // Name() == "hash-64"

	// Native chunk (embed_model "hash-64").
	native := env.putObject(t, "native.txt", "text/plain", "alpha")
	env.seedChunks(t, native, emb, "alpha")

	// Foreign-model chunk: same dimension, different embed_model.
	foreign := env.putObject(t, "foreign.txt", "text/plain", "alpha")
	vecs, err := emb.Embed(ctx, []string{"alpha"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if err := env.repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: foreign.ID, TenantID: foreign.TenantID, Bucket: foreign.Bucket, ObjectKey: foreign.Key,
		Seq: 0, Content: "alpha", Embedding: vecs[0], Dim: len(vecs[0]), EmbedModel: "other-model-64",
	}}); err != nil {
		t.Fatalf("insert foreign chunk: %v", err)
	}
	unknown := env.putObject(t, "unknown.txt", "text/plain", "alpha")
	if err := env.repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: unknown.ID, TenantID: unknown.TenantID, Bucket: unknown.Bucket, ObjectKey: unknown.Key,
		Seq: 0, Content: "alpha", Embedding: vecs[0], Dim: len(vecs[0]),
	}}); err != nil {
		t.Fatalf("insert unknown-model chunk: %v", err)
	}

	s := NewSearch(env.repo, emb, nil)
	hits, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "alpha", K: 10, Mode: "vector"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	nativeFound := false
	for _, h := range hits {
		if h.EmbedModel != emb.Name() {
			t.Fatalf("drift: a non-current-model chunk leaked into results: %+v", h)
		}
		if h.EmbedModel == "hash-64" {
			nativeFound = true
		}
	}
	if !nativeFound {
		t.Fatal("expected the query model's own chunk in results")
	}

	bm25 := NewBM25()
	for _, obj := range []repository.Object{native, foreign, unknown} {
		chunks, err := env.repo.ListChunksForObject(ctx, obj.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := bm25.UpsertObjectChunks(ctx, obj.ID, chunks); err != nil {
			t.Fatal(err)
		}
	}
	lexicalHits, err := NewSearch(env.repo, emb, nil).WithBM25(bm25).Query(
		ctx,
		Request{
			Tenant: testTenant, Bucket: testBucket,
			Query: "alpha", K: 10, Mode: "bm25",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(lexicalHits) != 1 || lexicalHits[0].ObjectID != native.ID {
		t.Fatalf("BM25 drift filter returned %+v, want only object %d", lexicalHits, native.ID)
	}
}
