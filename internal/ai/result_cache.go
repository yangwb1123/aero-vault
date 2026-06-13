package ai

import (
	"fmt"
	"sync"
	"time"
)

// resultCache is an opt-in, bounded, TTL'd cache of whole search results keyed
// by the normalized query parameters. It lets identical repeated queries skip
// the embed + retrieval + rerank work entirely.
//
// STALENESS: cached results can go stale as the corpus changes (new/edited/
// deleted chunks are not reflected until the entry expires). The TTL bounds how
// stale a result can be, which is why result caching is opt-in and defaults to
// a short TTL. It is bounded by capacity (arbitrary single-entry eviction when
// full) and safe for concurrent use.
type resultCache struct {
	capacity int
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]resultEntry
}

type resultEntry struct {
	hits   []Hit
	expiry time.Time
}

// newResultCache builds a cache; capacity<=0 or ttl<=0 returns nil so callers
// can treat a disabled cache as a simple nil check.
func newResultCache(capacity int, ttl time.Duration) *resultCache {
	if capacity <= 0 || ttl <= 0 {
		return nil
	}
	return &resultCache{capacity: capacity, ttl: ttl, cache: make(map[string]resultEntry, capacity)}
}

// key combines the fields that change a result. The \x1f (unit separator) can't
// appear in normal text, so distinct field tuples can't collide into one key.
func resultCacheKey(req Request) string {
	return fmt.Sprintf("%s\x1f%s\x1f%s\x1f%d\x1f%s", req.Tenant, req.Bucket, req.Mode, req.K, req.Query)
}

// get returns a COPY of the cached hits when a fresh (non-expired) entry exists.
// The copy ensures callers can't mutate the cached value.
func (c *resultCache) get(key string) ([]Hit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiry) {
		delete(c.cache, key)
		return nil, false
	}
	return cloneHits(e.hits), true
}

// put stores a COPY of hits with a fresh expiry. When at capacity it evicts an
// expired entry first (preserving live results) and falls back to an arbitrary
// live entry only when all entries are still valid.
func (c *resultCache) put(key string, hits []Hit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.cache[key]; !exists && len(c.cache) >= c.capacity {
		now := time.Now()
		evicted := false
		for ek, ev := range c.cache {
			if now.After(ev.expiry) {
				delete(c.cache, ek)
				evicted = true
				break
			}
		}
		if !evicted {
			for ek := range c.cache {
				delete(c.cache, ek)
				break
			}
		}
	}
	c.cache[key] = resultEntry{hits: cloneHits(hits), expiry: time.Now().Add(c.ttl)}
}

func cloneHits(hits []Hit) []Hit {
	if hits == nil {
		return nil
	}
	out := make([]Hit, len(hits))
	copy(out, hits)
	return out
}
