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
			out[i] = v
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
		out[idx] = vecs[j]
		c.put(missText[j], vecs[j])
	}
	c.mu.Unlock()
	return out, nil
}

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
