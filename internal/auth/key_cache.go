package auth

import (
	"sync"
	"time"
)

// keyCache is a bounded, TTL'd read-through cache for persisted API-key
// lookups, keyed by token hash. It memoizes only positive store hits (resolved
// Keys); misses fall through to the store, so revocation that happens in the
// store (locally) is reflected immediately via explicit invalidation, while
// cross-replica revokes are bounded by the TTL. It is bounded by capacity with
// arbitrary single-entry eviction (matching internal/ai/caching_embedder.go)
// and is safe for concurrent use.
type keyCache struct {
	ttl      time.Duration
	capacity int

	mu      sync.Mutex
	entries map[string]keyCacheEntry
}

type keyCacheEntry struct {
	key     Key
	expires time.Time
}

// newKeyCache returns a cache, or nil when disabled (ttl<=0 or capacity<=0).
func newKeyCache(ttl time.Duration, capacity int) *keyCache {
	if ttl <= 0 || capacity <= 0 {
		return nil
	}
	return &keyCache{ttl: ttl, capacity: capacity, entries: make(map[string]keyCacheEntry, capacity)}
}

// get returns the cached Key for hash when present and not yet expired.
func (c *keyCache) get(hash string, now time.Time) (Key, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[hash]
	if !ok {
		return Key{}, false
	}
	if now.After(e.expires) {
		delete(c.entries, hash)
		return Key{}, false
	}
	return e.key, true
}

// put caches k under hash. The entry expires at min(now+ttl, keyExpiry); a zero
// keyExpiry means the key never expires, so only the TTL bounds it.
func (c *keyCache) put(hash string, k Key, keyExpiry, now time.Time) {
	expires := now.Add(c.ttl)
	if !keyExpiry.IsZero() && keyExpiry.Before(expires) {
		expires = keyExpiry
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[hash]; !exists && len(c.entries) >= c.capacity {
		for ek := range c.entries {
			delete(c.entries, ek)
			break
		}
	}
	c.entries[hash] = keyCacheEntry{key: k, expires: expires}
}

// delete removes any entry for hash (used on revoke/add to invalidate stale data).
func (c *keyCache) delete(hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, hash)
}
