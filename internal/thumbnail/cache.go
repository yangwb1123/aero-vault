package thumbnail

import (
	"container/list"
	"sync"
)

// CacheKeyVersion is the cache-key schema version: bump it whenever the
// generated thumbnail bytes can change for the same source ETag + effective
// dims (pipeline output changes — quality, composite, rotation, format
// defaults). Stale entries under an old version are never looked up; the
// bounded LRU evicts them naturally.
const CacheKeyVersion = 1

// CacheKey identifies one cacheable thumbnail output: the tenant (cross-tenant
// isolation), the source object's content ETag (opaque — local storage uses
// hex MD5 of content), and the EFFECTIVE bounds EffectiveDims applies inside
// generateLocked, plus the key schema version. Bucket and object key are
// deliberately excluded: the output is a pure function of source bytes +
// effective dims, so two objects with identical bytes share one correct
// entry; the tenant component prevents one tenant's bytes ever being served
// to another tenant's requests.
type CacheKey struct {
	Tenant     string
	SourceETag string
	EffW, EffH int
	Version    uint8
}

// entry is one cached payload, linked into Cache's LRU list. data is
// cache-owned memory (a copy of what Put received); it is never mutated
// after insertion.
type entry struct {
	key  CacheKey
	data []byte
}

// Cache is a bounded, in-process LRU cache of generated thumbnail bytes,
// keyed by CacheKey. The byte budget (maxBytes) is enforced by evicting
// least-recently-used entries from the tail on overflow; a single payload
// larger than the whole budget (or empty) is never stored. All state is
// guarded by mu; the type is deterministic (no randomness, no timers) and
// safe for concurrent use under -race.
//
// A cache created with maxBytes <= 0 is disabled: Get always misses, Put is
// a no-op, Len/Bytes/Stats report zeros, and no state is allocated beyond
// the Cache struct itself (zero-allocation pass-through). disabled is
// immutable after NewCache (written before the pointer escapes), so Get and
// Put read it without locking.
type Cache struct {
	mu        sync.Mutex
	ll        *list.List
	m         map[CacheKey]*list.Element
	maxBytes  int64
	bytes     int64
	hits      uint64 // Stats surface (deterministic tests; telemetry forwards from the entry point)
	misses    uint64
	evictions uint64
	disabled  bool // immutable after NewCache
}

// NewCache returns a thumbnail output cache with the given byte budget, or a
// disabled pass-through when maxBytes <= 0 (THUMBNAIL_CACHE_BYTES=0 default).
func NewCache(maxBytes int64) *Cache {
	c := &Cache{
		maxBytes: maxBytes,
		disabled: maxBytes <= 0,
	}
	if !c.disabled {
		c.ll = list.New()
		c.m = make(map[CacheKey]*list.Element)
	}
	return c
}

// Get returns the cached payload for k. On a hit the entry's recency is
// refreshed (moved to the front of the LRU list) and the stored slice is
// returned by reference — callers must treat it as read-only so the byte
// budget stays exact (the sole production caller writes it only to the
// response). On a miss or on a disabled cache it returns (nil, false).
func (c *Cache) Get(k CacheKey) ([]byte, bool) {
	if c.disabled {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		c.ll.MoveToFront(el)
		c.hits++
		return el.Value.(*entry).data, true
	}
	c.misses++
	return nil, false
}

// Put stores a COPY of img under key k (the caller's slice is never
// retained, keeping the byte budget exact: bytes == Σ len(stored payloads))
// and returns the number of entries evicted to fit the budget. An empty
// payload is a strict no-op (entry untouched). A payload larger than the
// whole budget is refused: a superseded entry under the same key is removed
// (its payload must not be served as current) but no eviction is counted
// (a refusal, not budget pressure). An existing key is replaced in place
// (recency refreshed, no duplicate entry). On overflow, least-recently-used
// entries are evicted from the tail until bytes <= maxBytes.
func (c *Cache) Put(k CacheKey, img []byte) (evicted int) {
	if c.disabled || len(img) == 0 {
		return 0
	}
	if int64(len(img)) > c.maxBytes {
		// Refusal path: a payload that cannot fit within the whole budget is
		// never stored. If a previous payload exists under the key it is
		// removed — serving the old payload as current would be wrong once
		// the caller has a newer generation in hand.
		c.mu.Lock()
		defer c.mu.Unlock()
		if el, ok := c.m[k]; ok {
			e := el.Value.(*entry)
			c.ll.Remove(el)
			delete(c.m, k)
			c.bytes -= int64(len(e.data))
		}
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data := append([]byte(nil), img...)
	if el, ok := c.m[k]; ok {
		e := el.Value.(*entry)
		c.bytes += int64(len(data)) - int64(len(e.data))
		e.data = data
		c.ll.MoveToFront(el)
	} else {
		c.m[k] = c.ll.PushFront(&entry{key: k, data: data})
		c.bytes += int64(len(data))
	}
	for c.bytes > c.maxBytes {
		last := c.ll.Back()
		if last == nil {
			break
		}
		e := last.Value.(*entry)
		c.ll.Remove(last)
		delete(c.m, e.key)
		c.bytes -= int64(len(e.data))
		c.evictions++
		evicted++
	}
	return evicted
}

// Len returns the number of stored entries (0 for a disabled cache).
func (c *Cache) Len() int {
	if c.disabled {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// Bytes returns the total payload bytes stored (0 for a disabled cache).
func (c *Cache) Bytes() int64 {
	if c.disabled {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// Stats returns the cumulative hit/miss/eviction counters. A disabled cache
// always reports zeros — the entry point never consults or counts through it.
func (c *Cache) Stats() (hits, misses, evictions uint64) {
	if c.disabled {
		return 0, 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.evictions
}
