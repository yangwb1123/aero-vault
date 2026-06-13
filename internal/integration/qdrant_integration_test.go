//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func qdrantURL() string {
	if u := os.Getenv("AERO_QDRANT_URL"); u != "" {
		return u
	}
	return "http://localhost:6333"
}

// TestQdrantIntegration verifies the Qdrant adapter end-to-end against a live
// Qdrant instance: collection provisioning, chunk upsert, and nearest-neighbour
// retrieval. Run via `make test-integration-qdrant` or with a live Qdrant:
//
//	docker run -d --name aero-qdrant -p 6333:6333 qdrant/qdrant
//	go test -tags=integration ./internal/integration/ -v -run TestQdrant
//
// Override the URL with AERO_QDRANT_URL.
func TestQdrantIntegration(t *testing.T) {
	ctx := context.Background()
	base := qdrantURL()

	// Skip if Qdrant is not reachable.
	resp, err := http.Get(base + "/readyz") //nolint:noctx // intentional: probe only
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skipf("no Qdrant at %s (err=%v)", base, err)
	}
	if resp != nil {
		resp.Body.Close()
	}

	collection := fmt.Sprintf("aero_test_%d", testRunID())
	dim := 3

	idx := ai.NewQdrantIndex(ai.QdrantOptions{
		BaseURL:    base,
		Collection: collection,
	})

	// 1. Provision the collection.
	if err := idx.EnsureCollection(ctx, dim); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	// idempotent — calling again must not error.
	if err := idx.EnsureCollection(ctx, dim); err != nil {
		t.Fatalf("EnsureCollection (idempotent): %v", err)
	}

	// 2. Upsert two chunks with known embeddings.
	chunks := []repository.Chunk{
		{ID: 1, ObjectID: 42, TenantID: "default", Bucket: "default", ObjectKey: "a.txt", Seq: 0, Content: "near", Embedding: []float32{1, 0, 0}, Dim: dim, EmbedModel: "test"},
		{ID: 2, ObjectID: 42, TenantID: "default", Bucket: "default", ObjectKey: "a.txt", Seq: 1, Content: "far", Embedding: []float32{0, 1, 0}, Dim: dim, EmbedModel: "test"},
	}
	if err := idx.UpsertObjectChunks(ctx, 42, chunks); err != nil {
		t.Fatalf("UpsertObjectChunks: %v", err)
	}

	// 3. Search: query close to [1,0,0] should rank "near" first.
	hits, err := idx.SearchVectors(ctx, "default", "default", []float32{0.9, 0.1, 0}, 5)
	if err != nil {
		t.Fatalf("SearchVectors: %v", err)
	}
	if len(hits) < 1 {
		t.Fatal("expected at least 1 hit, got none")
	}
	if hits[0].Chunk.Content != "near" {
		t.Fatalf("expected 'near' as top hit, got %q", hits[0].Chunk.Content)
	}

	// 4. Delete chunks for object 42 and verify search returns nothing.
	if err := idx.DeleteObjectChunks(ctx, 42); err != nil {
		t.Fatalf("DeleteObjectChunks: %v", err)
	}
	hits2, err := idx.SearchVectors(ctx, "default", "default", []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("SearchVectors after delete: %v", err)
	}
	if len(hits2) != 0 {
		t.Fatalf("expected 0 hits after delete, got %d", len(hits2))
	}
}

// testRunID returns a simple collision-avoiding suffix using the process ID so
// each test run gets its own collection name and doesn't collide with others.
func testRunID() int {
	return os.Getpid()
}
