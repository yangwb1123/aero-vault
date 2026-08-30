package thumbnail

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// CacheKeyVersion is the cache/wire schema family version. Version 2 binds
// entries to the complete authoritative source identity, source ETag,
// effective dimensions, and the output representation token; old entries are
// never looked up. Representation changes are tracked independently so they
// do not rely on manually bumping this schema version.
const CacheKeyVersion = 2

// GetOutcome classifies one Cache.Get result. GetHit: stored bytes served,
// LRU recency refreshed (expiry deadline NOT extended — fixed from the last
// Put). GetMiss: genuine miss — key absent, or disabled cache (counted by
// nothing on the disabled path). GetExpired: the entry existed but
// now.After(expiresAt); it is removed here and NOT served, and it is neither
// a miss (the hit-ratio class) nor an LRU eviction — it increments Cache's
// expired counter / thumbnail.cache.expired_total.
type GetOutcome uint8

const (
	GetHit     GetOutcome = iota // served from cache; recency refreshed
	GetMiss                      // genuine miss: absent key, or disabled cache
	GetExpired                   // entry existed but now.After(expiresAt); removed here, not served
)

// CacheKey identifies one cacheable thumbnail output. Identity fields remain
// separate so tenant, bucket, key, and generation cannot alias at delimiters.
// An incomplete identity is never looked up or stored.
type CacheKey struct {
	Identity       SourceIdentity
	SourceETag     string
	EffW, EffH     int
	Version        uint8
	Representation string
}

// entry is one cached payload, linked into Cache's LRU list. data is
// cache-owned memory (a copy of what Put received); it is never mutated
// after insertion. expiresAt is the retention deadline set by the last Put
// (fresh expiry per generation); the zero value is never consulted when the
// owning cache has ttl <= 0.
type entry struct {
	key       CacheKey
	data      []byte
	expiresAt time.Time
}

// Cache is a bounded, in-process LRU cache of generated thumbnail bytes,
// keyed by CacheKey. The byte budget (maxBytes) is enforced by reclaiming
// resident expired entries first when ttl > 0 and a Put overflows, then by
// evicting least-recently-used live entries from the tail if needed; a
// single payload larger than the whole budget (or empty) is never stored.
// LRU state and counters are guarded by mu; cached-generation flights are
// guarded by flightMu. The type spawns no goroutines and owns no timers.
// When ttl > 0, Get/Put perform monotonic wall-clock comparisons inside the
// critical sections; when ttl <= 0, no wall-clock reads occur on any path.
// Raw Get/Put remain LRU primitives; flights are used only by the cached
// generation entry point. The type is otherwise deterministic and safe for
// concurrent use under -race.
//
// A cache created with maxBytes <= 0 is disabled: Get always misses, Put is
// a no-op, Len/Bytes/Stats report zeros, and no state is allocated beyond
// the Cache struct itself (zero-allocation pass-through). disabled and ttl
// are immutable after NewCache (written before the pointer escapes), so Get
// and Put read them without locking.
//
// Retention model (THUMBNAIL_CACHE_TTL): when ttl > 0, every stored entry
// expires that long after the Put that produced it — a fixed TTL from the
// last store, never extended by hits — and is never served after expiry
// (lazy expiry on Get; an expired read is a distinct outcome — removed, not
// served, counted in the cache's expired counter / thumbnail.cache.expired_total,
// never in the hit-ratio miss class). When ttl <= 0
// (default) entries live until LRU byte-budget pressure evicts them,
// byte-for-byte the pre-TTL behavior. When ttl > 0, a Put that overflows
// first reclaims resident expired entries without counting them as
// evictions, then falls back to live tail eviction only if needed. Lazy
// expiry bounds served retention strictly; SweepExpired additionally bounds
// physical retention of never-read expired keys. cmd/server owns the timer
// driver and runs this pass whenever TTL is enabled; Cache itself still owns
// no goroutines/timers.
//
// FR-4 (campaign add-ttl-lifecycle-invalidation-to-the-thumbnail) is
// implemented below: SweepExpired walks the LRU list under c.mu removing
// entries with now.After(e.expiresAt), decrementing c.bytes by exactly
// len(e.data) each, returning the count for telemetry. It is cheap (single
// O(entries) pass, zero allocations), does not bump the LRU eviction
// counter, and must never be invoked from the request path; the natural
// driver is an existing timer loop (e.g. the Reconcile ticker, AGENTS.md
// §2.4), never a goroutine owned by Cache. cmd/server/thumbnail_sweep.go calls
// it on its configured cadence; the cache remains a library type with no
// background lifecycle of its own.
type Cache struct {
	mu        sync.Mutex
	ll        *list.List
	m         map[CacheKey]*list.Element
	maxBytes  int64
	bytes     int64
	hits      uint64 // Stats surface (deterministic tests; telemetry forwards from the entry point)
	misses    uint64
	evictions uint64
	expired   uint64        // TTL-lazy-expiry removals; distinct from misses (hit-ratio class) and evictions
	disabled  bool          // immutable after NewCache
	ttl       time.Duration // immutable after NewCache; <= 0 = no expiry
	flightMu  sync.Mutex
	flights   map[CacheKey]*cacheFlight
}

// NewCache returns a thumbnail output cache with the given byte budget and
// per-entry retention TTL, or a disabled pass-through when maxBytes <= 0
// (THUMBNAIL_CACHE_BYTES=0 default). ttl <= 0 disables expiry: entries live
// until LRU byte-budget pressure evicts them (THUMBNAIL_CACHE_TTL=0 default)
// — byte-for-byte the pre-TTL behavior, including no wall-clock reads on
// any path. When ttl > 0, every stored entry expires that long after the Put
// that produced it (fixed TTL from the last store; hits do not extend it)
// and is never served after expiry.
func NewCache(maxBytes int64, ttl time.Duration) *Cache {
	c := &Cache{
		maxBytes: maxBytes,
		ttl:      ttl,
		disabled: maxBytes <= 0,
	}
	if !c.disabled {
		c.ll = list.New()
		c.m = make(map[CacheKey]*list.Element)
	}
	return c
}

// Enabled reports whether this cache has a positive byte budget. It lets
// adapter-level observability distinguish an intentionally disabled cache
// from an enabled cache that bypassed a structurally uncacheable request.
func (c *Cache) Enabled() bool {
	return c != nil && !c.disabled
}

// PayloadFits reports whether img could be stored under this cache's byte
// budget. It is a read-only companion to Put for the store-refusal telemetry
// path; it does not reserve space or inspect the LRU.
func (c *Cache) PayloadFits(img []byte) bool {
	return c != nil && !c.disabled && len(img) > 0 && int64(len(img)) <= c.maxBytes
}

// Get returns the cached payload for k classified by GetOutcome. On a hit
// the entry's recency is refreshed (moved to the front of the LRU list) and
// the stored slice is returned by reference — callers must treat it as
// read-only so the byte budget stays exact (the sole production caller
// writes it only to the response). Expiry is NOT extended on hits: the
// retention deadline is set by the last Put (a hit proves the entry is being
// served, but the deadline is fixed by the generation that produced it —
// this is what guarantees no entry is served beyond TTL after its producing
// generation). On a TTL-expired entry it returns GetExpired (removed here —
// not a miss, not an LRU eviction); on a genuine miss or a disabled cache it
// returns GetMiss with nil bytes.
func (c *Cache) Get(k CacheKey) ([]byte, GetOutcome) {
	data, outcome, _ := c.getContext(nil, k)
	return data, outcome
}

// peekContext observes a live entry without changing LRU or hit/miss state.
// It closes the gap between an initial miss and flight ownership without
// double-counting a miss when another leader stored the result meanwhile.
func (c *Cache) peekContext(ctx context.Context, k CacheKey) ([]byte, bool, error) {
	if c == nil || c.disabled || !k.Identity.Complete() {
		return nil, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[k]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*entry)
	if c.ttl > 0 && time.Now().After(e.expiresAt) {
		return nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return e.data, true, nil
}

// getContext is the internal entry point for request-cancellable lookups.
// Callers that need cancellation-aware semantics must use this helper, not
// Cache.Get followed by a post-return ctx.Err() check — the hit-side effects
// commit under c.mu. A nil ctx disables the veto for context-oblivious callers
// such as Cache.Get.
func (c *Cache) getContext(ctx context.Context, k CacheKey) ([]byte, GetOutcome, error) {
	if c == nil || c.disabled || !k.Identity.Complete() {
		return nil, GetMiss, nil // counted by nothing; lookupCached guards this path
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		e := el.Value.(*entry)
		if c.ttl > 0 && time.Now().After(e.expiresAt) {
			// TTL-expired: remove exactly (map delete + list removal) and
			// decrement the byte accounting; report GetExpired. This is not
			// an LRU eviction — the eviction counter is untouched, and the
			// expired entry must not earn a fresh LRU lease — and it is not
			// a miss (the hit-ratio class): the expired class is its own
			// counter, forwarded by lookupCached to thumbnail.cache.expired_total.
			c.removeElementLocked(el)
			c.expired++
			return nil, GetExpired, nil
		}
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, GetHit, err
			}
		}
		c.ll.MoveToFront(el)
		c.hits++
		return e.data, GetHit, nil
	}
	c.misses++
	return nil, GetMiss, nil
}

// Put stores a COPY of img under key k (the caller's slice is never
// retained, keeping the byte budget exact: bytes == Σ len(stored payloads))
// and returns the number of live entries evicted to fit the budget. An empty
// payload is a strict no-op (entry untouched). A payload larger than the
// whole budget is refused: a superseded entry under the same key is removed
// (its payload must not be served as current) but no eviction is counted
// (a refusal, not budget pressure). An existing key is replaced in place
// (recency refreshed, no duplicate entry; a replacement is a new generation
// → a fresh expiry when ttl > 0). When ttl <= 0, no wall-clock reads occur
// on this path. When ttl > 0 and the store overflows, resident expired
// entries are reclaimed first without counting them as evictions; live
// least-recently-used entries are evicted from the tail only if bytes still
// exceed maxBytes.
func (c *Cache) Put(k CacheKey, img []byte) (evicted int) {
	if c == nil || c.disabled || !k.Identity.Complete() || len(img) == 0 {
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
			c.removeElementLocked(el)
		}
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data := append([]byte(nil), img...)
	// A fresh retention deadline per store; the zero value when ttl <= 0 is
	// never consulted by Get (guarded by c.ttl > 0), so the non-expiring path
	// performs no wall-clock read.
	var (
		now       time.Time
		expiresAt time.Time
	)
	if c.ttl > 0 {
		now = time.Now()
		expiresAt = now.Add(c.ttl) // monotonic clock participates in comparisons
	}
	if el, ok := c.m[k]; ok {
		e := el.Value.(*entry)
		c.bytes += int64(len(data)) - int64(len(e.data))
		e.data = data
		e.expiresAt = expiresAt
		c.ll.MoveToFront(el)
	} else {
		c.m[k] = c.ll.PushFront(&entry{key: k, data: data, expiresAt: expiresAt})
		c.bytes += int64(len(data))
	}
	if c.ttl > 0 && c.bytes > c.maxBytes {
		c.reclaimExpiredLocked(now)
	}
	for c.bytes > c.maxBytes {
		last := c.ll.Back()
		if last == nil {
			break
		}
		c.removeElementLocked(last)
		c.evictions++
		evicted++
	}
	return evicted
}

func (c *Cache) removeElementLocked(el *list.Element) *entry {
	e := el.Value.(*entry)
	c.ll.Remove(el)
	delete(c.m, e.key)
	c.bytes -= int64(len(e.data))
	return e
}

// reclaimExpiredLocked removes resident entries whose retention deadline has
// passed according to the caller-supplied clock. The caller must hold c.mu.
// It preserves the LRU order of survivors, decrements c.bytes by exactly
// len(e.data) per removal, and does not touch hit/miss/eviction/expired
// counters because this physical purge is neither a read nor a live LRU
// eviction.
func (c *Cache) reclaimExpiredLocked(now time.Time) (removed int) {
	for el := c.ll.Front(); el != nil; {
		next := el.Next()
		e := el.Value.(*entry)
		if now.After(e.expiresAt) {
			c.removeElementLocked(el)
			removed++
		}
		el = next
	}
	return removed
}

// SweepExpired removes every stored entry whose retention deadline has
// passed, returning the number of entries removed. It is the physical purge
// half of the TTL contract: Get's lazy branch bounds served retention (an
// expired entry is reclaimed only when a request touches its exact key),
// while SweepExpired bounds physical retention of never-read expired keys
// (to TTL + sweep interval once a caller wires a timer driver).
//
// It walks the LRU list exactly once under a single c.mu acquisition
// (O(entries), zero allocations), removing exactly the entries with
// now.After(e.expiresAt) — the same strict-after predicate Get's lazy
// branch uses, so "expired" has one definition across both paths. Each
// removal decrements c.bytes by exactly len(e.data) (invariant: bytes ==
// Σ len(stored payloads)) and does not touch the hit/miss/eviction/expired
// counters: an expired removal by sweep is not a read (the lazy path's
// expired counting does not apply) and it is not an LRU eviction. Live entries
// (including the boundary expiresAt == now) are left completely alone — no
// recency refresh, no reorder; the LRU order of survivors is unchanged and
// they are still served by Get with byte-identical payloads.
//
// A disabled cache (maxBytes <= 0) and a cache with ttl <= 0 return 0
// immediately without acquiring c.mu and without consulting any expiresAt:
// the zero value is never consulted when the owning cache has ttl <= 0, and
// a naive walk would evaluate now.After(time.Time{}) == true and wipe a
// non-TTL cache. now is the caller's clock (time.Now() in production;
// injected values in tests); the monotonic component participates in the
// comparison, matching Put's expiresAt = time.Now().Add(c.ttl). It must
// never be invoked from the request path — the natural driver is an
// existing timer loop (e.g. the Reconcile ticker, AGENTS.md §2.4), never a
// goroutine owned by Cache. The returned count is for the caller's
// telemetry; Cache.Stats is untouched.
func (c *Cache) SweepExpired(now time.Time) (n int) {
	if c.disabled || c.ttl <= 0 {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reclaimExpiredLocked(now)
}

// InvalidateSource removes every resident entry derived from the exact
// authoritative source generation id, regardless of ETag, dimensions, cache
// schema version, representation, or TTL state. It is a resident-only purge:
// disabled caches and incomplete identities are strict no-ops, and in-flight
// coalesced generations are intentionally untouched.
func (c *Cache) InvalidateSource(id SourceIdentity) (removed int) {
	if c == nil || c.disabled || !id.Complete() {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.ll.Front(); el != nil; {
		next := el.Next()
		e := el.Value.(*entry)
		if e.key.Identity.Equal(id) {
			c.removeElementLocked(el)
			removed++
		}
		el = next
	}
	return removed
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

// Stats returns the cumulative hit/miss/eviction/expired counters. A disabled
// cache always reports zeros — the entry point never consults or counts
// through it. expired counts lazy TTL-expiry removals only; SweepExpired
// removals and Put-side expired reclamation are physical purges, not reads,
// and touch none of these counters.
func (c *Cache) Stats() (hits, misses, evictions, expired uint64) {
	if c.disabled {
		return 0, 0, 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.evictions, c.expired
}
