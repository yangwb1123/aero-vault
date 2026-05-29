package ai

import (
	"context"
	"testing"
)

// countingEmbedder records how many texts it actually embedded.
type countingEmbedder struct {
	dim      int
	embedded int
}

func (c *countingEmbedder) Dimensions() int { return c.dim }
func (c *countingEmbedder) Name() string    { return "counting" }
func (c *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.embedded += len(texts)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, c.dim)
	}
	return out, nil
}

func TestCachingEmbedder_MemoizesByText(t *testing.T) {
	ctx := context.Background()
	inner := &countingEmbedder{dim: 4}
	emb := NewCachingEmbedder(inner, 16)

	if _, err := emb.Embed(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	// "a" is cached; only "b" should reach the inner embedder.
	if _, err := emb.Embed(ctx, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if inner.embedded != 2 { // "a" once + "b" once, never "a" twice
		t.Fatalf("inner embedded %d texts, want 2 (a once, b once)", inner.embedded)
	}
	// Repeat of cached texts → no further inner calls.
	if _, err := emb.Embed(ctx, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if inner.embedded != 2 {
		t.Fatalf("cached repeat should not re-embed; inner=%d want 2", inner.embedded)
	}
}

func TestNewCachingEmbedder_PassThroughWhenDisabled(t *testing.T) {
	inner := &countingEmbedder{dim: 4}
	if got := NewCachingEmbedder(inner, 0); got != Embedder(inner) {
		t.Fatal("capacity<=0 must return the inner embedder unchanged")
	}
	if got := NewCachingEmbedder(nil, 8); got != nil {
		t.Fatal("nil inner must return nil")
	}
}
