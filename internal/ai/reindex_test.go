package ai

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestReindexStale re-indexes objects whose chunks were embedded by a different
// model, so after the run the corpus uses the current embedder's model.
func TestReindexStale(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64) // Name() == "hash-64"

	// Object with a blob, but chunks recorded under a STALE embed model.
	obj := env.putObject(t, "doc.txt", "text/plain", "alpha beta gamma")
	if err := env.repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key,
		Seq: 0, Content: "alpha beta gamma", Embedding: []float32{0, 0, 0}, Dim: 3, EmbedModel: "old-model",
	}}); err != nil {
		t.Fatalf("seed stale chunk: %v", err)
	}

	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil)

	n, err := ix.ReindexStale(ctx, testTenant, 100)
	if err != nil {
		t.Fatalf("ReindexStale: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 object reindexed, got %d", n)
	}

	// All chunks for the object now carry the current model; none are stale.
	chunks, err := env.repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected re-indexed chunks")
	}
	for _, c := range chunks {
		if c.EmbedModel != emb.Name() {
			t.Fatalf("chunk still on stale model %q (want %q)", c.EmbedModel, emb.Name())
		}
	}
	// And the stale list is now empty.
	stale, err := env.repo.ListObjectIDsToReindex(ctx, testTenant, emb.Name(), 100)
	if err != nil {
		t.Fatalf("list stale: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("expected no stale objects after reindex, got %v", stale)
	}
}
