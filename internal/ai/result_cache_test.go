package ai

import (
	"context"
	"testing"
	"time"
)

// countingHashEmbedder wraps a HashEmbedder and counts how many texts it
// actually embeds, so we can prove the result cache short-circuits work. It
// produces real (deterministic) vectors so vector search has something to
// match, unlike the zero-vector countingEmbedder in caching_embedder_test.go.
type countingHashEmbedder struct {
	inner    Embedder
	embedded int
}

func (c *countingHashEmbedder) Dimensions() int { return c.inner.Dimensions() }
func (c *countingHashEmbedder) Name() string    { return c.inner.Name() }
func (c *countingHashEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c.embedded += len(texts)
	return c.inner.Embed(ctx, texts)
}

func TestSearchResultCache_ShortCircuitsRepeatQuery(t *testing.T) {
	env := newTestEnv(t)
	emb := &countingHashEmbedder{inner: NewHashEmbedder(128)}
	o := env.putObject(t, "rc.txt", "text/plain", "result cache")
	env.seedChunks(t, o, emb,
		"reciprocal rank fusion combines result lists",
		"the weather is sunny today with clear skies",
	)
	// Reset count: seeding embedded the chunks, not the query.
	emb.embedded = 0

	s := NewSearch(env.repo, emb, nil).WithResultCache(16, time.Minute)
	req := Request{Tenant: testTenant, Query: "rank fusion result lists", K: 5, Mode: "vector"}

	first, err := s.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("first query: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("expected at least one hit")
	}

	second, err := s.Query(context.Background(), req)
	if err != nil {
		t.Fatalf("second query: %v", err)
	}
	if len(second) != len(first) {
		t.Fatalf("cached result length mismatch: %d vs %d", len(second), len(first))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("hit %d differs between calls: %+v vs %+v", i, first[i], second[i])
		}
	}

	// The cache must short-circuit the embed work: the query is embedded once.
	if emb.embedded != 1 {
		t.Fatalf("expected embedder invoked once across two identical queries, got %d", emb.embedded)
	}
}

func TestSearchResultCache_ZeroCapacityIsPassThrough(t *testing.T) {
	env := newTestEnv(t)
	emb := &countingHashEmbedder{inner: NewHashEmbedder(128)}
	o := env.putObject(t, "rc0.txt", "text/plain", "no cache")
	env.seedChunks(t, o, emb, "alpha beta gamma delta retrieval")
	emb.embedded = 0

	// capacity<=0 leaves caching disabled (no-op).
	s := NewSearch(env.repo, emb, nil).WithResultCache(0, time.Minute)
	req := Request{Tenant: testTenant, Query: "alpha beta retrieval", K: 5, Mode: "vector"}

	if _, err := s.Query(context.Background(), req); err != nil {
		t.Fatalf("first query: %v", err)
	}
	if _, err := s.Query(context.Background(), req); err != nil {
		t.Fatalf("second query: %v", err)
	}
	if emb.embedded != 2 {
		t.Fatalf("zero-capacity cache should be pass-through; embedder called %d times, want 2", emb.embedded)
	}
}
