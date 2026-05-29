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

	s := NewSearch(env.repo, emb, nil)
	hits, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "alpha", K: 10, Mode: "vector"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	nativeFound := false
	for _, h := range hits {
		if h.EmbedModel == "other-model-64" {
			t.Fatalf("drift: a foreign-model chunk leaked into results: %+v", h)
		}
		if h.EmbedModel == "hash-64" {
			nativeFound = true
		}
	}
	if !nativeFound {
		t.Fatal("expected the query model's own chunk in results")
	}
}
