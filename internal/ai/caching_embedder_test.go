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

// valueEmbedder returns a deterministic non-zero vector per text so mutations
// are detectable (the counting embedder hands back all-zero vectors).
type valueEmbedder struct{ dim int }

func (v *valueEmbedder) Dimensions() int { return v.dim }
func (v *valueEmbedder) Name() string    { return "value" }
func (v *valueEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec := make([]float32, v.dim)
		for j := range vec {
			vec[j] = float32(len(t) + j + 1)
		}
		out[i] = vec
	}
	return out, nil
}

// Mutating a returned vector must not corrupt the cache: a later cache hit for
// the same text must still see the original values.
func TestCachingEmbedder_ReturnedVectorMutationDoesNotCorruptCache(t *testing.T) {
	ctx := context.Background()
	emb := NewCachingEmbedder(&valueEmbedder{dim: 4}, 16)

	first, err := emb.Embed(ctx, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]float32(nil), first[0]...)

	// Caller scribbles over the returned slice.
	for i := range first[0] {
		first[0][i] = -999
	}

	// Subsequent cache hit must be unaffected by the mutation above.
	second, err := emb.Embed(ctx, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if second[0][i] != want[i] {
			t.Fatalf("cache hit corrupted at index %d: got %v want %v (full=%v)", i, second[0][i], want[i], second[0])
		}
	}

	// And mutating the second result must not corrupt a third hit either —
	// confirms each return is its own copy, not a shared alias.
	second[0][0] = 42
	third, err := emb.Embed(ctx, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if third[0][0] != want[0] {
		t.Fatalf("returned vectors alias each other: third=%v want %v", third[0], want)
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
