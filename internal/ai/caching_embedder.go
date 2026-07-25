package ai

import (
	"context"
	"sync"
)

// cachingEmbedder memoizes embeddings by input text to cut repeat latency and
// provider cost — query embeddings in particular recur heavily. It is bounded
// by capacity (arbitrary single-entry eviction when full) and safe for
// concurrent use. Cache keys are plain text; since each instance wraps exactly
// one underlying model, no model qualifier is needed.
type cachingEmbedder struct {
	inner    Embedder
	capacity int

	mu    sync.Mutex
	cache map[string][]float32
}

// NewCachingEmbedder wraps inner with an in-memory bounded cache. capacity<=0
// (or a nil inner) returns inner unchanged, so callers can wire it
// unconditionally.
func NewCachingEmbedder(inner Embedder, capacity int) Embedder {
	if inner == nil || capacity <= 0 {
		return inner
	}
	return &cachingEmbedder{inner: inner, capacity: capacity, cache: make(map[string][]float32, capacity)}
}

func (c *cachingEmbedder) Dimensions() int { return c.inner.Dimensions() }
func (c *cachingEmbedder) Name() string    { return c.inner.Name() }

func (c *cachingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	var missIdx []int
	var missText []string

	c.mu.Lock()
	for i, t := range texts {
		if v, ok := c.cache[t]; ok {
			// Hand back an independent copy; callers must not be able to mutate
			// the cached vector (and vice-versa).
			out[i] = cloneVec(v)
		} else {
			missIdx = append(missIdx, i)
			missText = append(missText, t)
		}
	}
	c.mu.Unlock()

	if len(missText) == 0 {
		return out, nil
	}

	vecs, err := c.inner.Embed(ctx, missText)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for j, idx := range missIdx {
		// Cache and output must be independent: store a clone and return the
		// other, so neither the caller nor a later cache hit can corrupt the
		// other's vector.
		out[idx] = vecs[j]
		c.put(missText[j], cloneVec(vecs[j]))
	}
	c.mu.Unlock()
	return out, nil
}

// cloneVec returns an independent copy of v so cached and returned vectors
// never alias each other.
func cloneVec(v []float32) []float32 { return append([]float32(nil), v...) }

// put inserts with naive capacity bounding: evict one arbitrary entry when full.
func (c *cachingEmbedder) put(k string, v []float32) {
	if len(c.cache) >= c.capacity {
		for ek := range c.cache {
			delete(c.cache, ek)
			break
		}
	}
	c.cache[k] = v
}
