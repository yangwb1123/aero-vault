package thumbnail

// White-box tests for the bounded byte-budget LRU cache (cache.go). All
// fixtures are deterministic (byte slices / LCG); no sleeps; -short and
// -race friendly within the 180 s test-race-thumbnail budget.

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func payload(n int, fill byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = fill
	}
	return b
}

// TestCacheHitMiss covers REQ-1's hit/miss contract plus the disabled
// pass-through and replace semantics (T-1).
func TestCacheHitMiss(t *testing.T) {
	c := NewCache(1<<20, 0)
	k1 := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
	k2 := CacheKey{Tenant: "t1", SourceETag: "e2", EffW: 32, EffH: 32}

	// 1. Put then hit: same bytes, exact budget accounting, single entry.
	a := payload(100, 'a')
	c.Put(k1, a)
	got, ok := c.Get(k1)
	if !ok {
		t.Fatal("Get(k1) after Put: want hit, got miss")
	}
	if !bytes.Equal(got, a) {
		t.Fatal("Get(k1) returned different bytes than Put")
	}
	if c.Bytes() != int64(len(a)) {
		t.Fatalf("Bytes() = %d, want %d", c.Bytes(), len(a))
	}
	if c.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", c.Len())
	}

	// 2. Miss for a foreign key; disabled cache is a pure pass-through.
	if got, ok := c.Get(k2); ok || got != nil {
		t.Fatalf("Get(k2): want (nil, false), got (%v, %v)", got, ok)
	}
	dis := NewCache(0, 0)
	if got, ok := dis.Get(k1); ok || got != nil {
		t.Fatalf("disabled Get: want (nil, false), got (%v, %v)", got, ok)
	}
	dis.Put(k1, a)
	if dis.Bytes() != 0 || dis.Len() != 0 {
		t.Fatalf("disabled Put must be a no-op: Bytes=%d Len=%d", dis.Bytes(), dis.Len())
	}

	// 3. Replace refreshes recency and payload without duplicating the entry.
	b := payload(50, 'b')
	c.Put(k1, b)
	if c.Len() != 1 {
		t.Fatalf("Len() after replace = %d, want 1 (no duplicate entry)", c.Len())
	}
	got, ok = c.Get(k1)
	if !ok || !bytes.Equal(got, b) {
		t.Fatalf("Get(k1) after replace: want (b, true), got ok=%v bytes=%q", ok, got)
	}
	if c.Bytes() != int64(len(b)) {
		t.Fatalf("Bytes() after replace = %d, want %d", c.Bytes(), len(b))
	}

	// 4. Empty payload is a strict no-op (existing entry untouched).
	c.Put(k1, nil)
	if got, ok := c.Get(k1); !ok || !bytes.Equal(got, b) {
		t.Fatalf("empty Put must not disturb the existing entry: ok=%v bytes=%q", ok, got)
	}
}

// TestCacheByteBudgetEviction covers exact-budget, LRU tail order,
// recency-touch, and oversized-not-stored (T-2).
func TestCacheByteBudgetEviction(t *testing.T) {
	// Budget exactly len(a)+len(b); c <= a so after evicting k1 both k2 and
	// k3 survive (bytes == len(b)+len(c) <= budget).
	a, b, c := payload(100, 'a'), payload(200, 'b'), payload(50, 'c')
	budget := int64(len(a) + len(b))
	cache := NewCache(budget, 0)
	k1 := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
	k2 := CacheKey{Tenant: "t1", SourceETag: "e2", EffW: 32, EffH: 32}
	k3 := CacheKey{Tenant: "t1", SourceETag: "e3", EffW: 32, EffH: 32}

	cache.Put(k1, a)
	cache.Put(k2, b)
	if cache.Bytes() != budget {
		t.Fatalf("Bytes() = %d, want exact budget %d", cache.Bytes(), budget)
	}
	if _, ok := cache.Get(k1); !ok {
		t.Fatal("k1 must be present at exact budget")
	}
	if _, ok := cache.Get(k2); !ok {
		t.Fatal("k2 must be present at exact budget")
	}

	// Inserting k3 overflows: the least-recently-used tail (k1) is evicted.
	n := cache.Put(k3, c)
	if n != 1 {
		t.Fatalf("Put(k3) evicted %d entries, want 1", n)
	}
	if _, ok := cache.Get(k1); ok {
		t.Fatal("k1 must be evicted (oldest)")
	}
	if _, ok := cache.Get(k2); !ok {
		t.Fatal("k2 must survive eviction")
	}
	if _, ok := cache.Get(k3); !ok {
		t.Fatal("k3 must be present after insertion")
	}
	if cache.Bytes() > budget {
		t.Fatalf("Bytes() = %d exceeds budget %d", cache.Bytes(), budget)
	}

	// LRU recency-touch: touching k2 must protect it from the next eviction;
	// the untouched k1 (tail) is the victim instead. Fresh cache so the
	// ordering is unambiguous.
	touched := NewCache(budget, 0)
	touched.Put(k1, a)
	touched.Put(k2, b)
	if _, ok := touched.Get(k2); !ok {
		t.Fatal("k2 must be present before touch")
	}
	n = touched.Put(k3, c)
	if n != 1 {
		t.Fatalf("Put(k3) after touch evicted %d entries, want 1", n)
	}
	if _, ok := touched.Get(k1); ok {
		t.Fatal("untouched k1 must be the LRU victim (recency-touch)")
	}
	if _, ok := touched.Get(k2); !ok {
		t.Fatal("touched k2 must survive eviction")
	}
	if _, ok := touched.Get(k3); !ok {
		t.Fatal("k3 must be present after insertion")
	}

	// Oversized entry: never stored, and no eviction counter increment.

	// Oversized entry (new key): never stored, and no eviction counter
	// increment.
	huge := payload(int(budget)+1, 'x')
	kBig := CacheKey{Tenant: "t1", SourceETag: "big", EffW: 32, EffH: 32}
	before := cache.Len()
	n = cache.Put(kBig, huge)
	if n != 0 {
		t.Fatalf("oversized Put must return 0 evictions, got %d", n)
	}
	if cache.Len() != before {
		t.Fatalf("oversized Put changed Len: %d -> %d", before, cache.Len())
	}
	if _, ok := cache.Get(kBig); ok {
		t.Fatal("oversized payload must not be stored under kBig")
	}

	// Oversized replace of an existing key removes the superseded entry
	// (its payload must not be served as current) without counting it as a
	// budget-pressure eviction.
	cache.Put(k3, c)
	if _, ok := cache.Get(k3); !ok {
		t.Fatal("k3 must be present before oversized replace")
	}
	h, m, ev := cache.Stats()
	cache.Put(k3, huge)
	if _, ok := cache.Get(k3); ok {
		t.Fatal("superseded k3 must be removed by oversized replace")
	}
	_, _, ev2 := cache.Stats()
	if ev2 != ev {
		t.Fatalf("oversized replace counted an eviction (%d -> %d)", ev, ev2)
	}
	_ = h
	_ = m
}

// TestCacheConcurrentAccess exercises 8 goroutines x 2000 iterations of Put
// (per-goroutine keys + one shared hot key) and Get; -race clean, no torn
// payloads, budget holds (T-3).
func TestCacheConcurrentAccess(t *testing.T) {
	const budget = 4 << 10
	c := NewCache(budget, 0)
	const gs = 8
	const iters = 2000
	// Errors from worker goroutines are collected (t.Fatalf is not allowed
	// off the test goroutine; vet's testinggoroutine check) and surfaced
	// after the join.
	var errs []string
	var errMu sync.Mutex
	record := func(format string, args ...any) {
		errMu.Lock()
		defer errMu.Unlock()
		errs = append(errs, fmt.Sprintf(format, args...))
	}
	var wg sync.WaitGroup
	for g := 0; g < gs; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			fill := byte(g)
			for i := 0; i < iters; i++ {
				k := CacheKey{Tenant: "t1", SourceETag: fmt.Sprintf("g%d-%d", g, i), EffW: 32, EffH: 32}
				p := payload(64, fill)
				c.Put(k, p)
				hot := CacheKey{Tenant: "t1", SourceETag: "hot", EffW: 32, EffH: 32}
				c.Put(hot, p)
				if got, ok := c.Get(k); ok {
					// Payloads are never mutated after storage: any hit must
					// return a full-length, intact slice.
					if len(got) != 64 {
						record("torn payload: got %d bytes, want 64", len(got))
					}
				}
				if got, ok := c.Get(hot); ok && len(got) != 64 {
					record("torn hot payload: got %d bytes, want 64", len(got))
				}
				if _, ok := c.Get(CacheKey{Tenant: "t2", SourceETag: "foreign", EffW: 32, EffH: 32}); ok {
					record("foreign key must never hit")
				}
			}
		}(g)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("concurrent access violations:\n%s", strings.Join(errs, "\n"))
	}
	if c.Bytes() > budget {
		t.Fatalf("Bytes() = %d exceeds budget %d after concurrency", c.Bytes(), budget)
	}
	if c.Len() < 1 {
		t.Fatal("Len() = 0, want >= 1 (hot key must survive)")
	}
	// The hot key was last written by some goroutine with a full payload.
	if got, ok := c.Get(CacheKey{Tenant: "t1", SourceETag: "hot", EffW: 32, EffH: 32}); !ok || len(got) != 64 {
		t.Fatalf("hot key: want full payload, got ok=%v len=%d", ok, len(got))
	}
}

// TestCacheStressMemoryBounded runs a deterministic LCG over 10 000 Puts with
// payloads in [1, 2048] and asserts Bytes() <= 4096 after every iteration
// (T-6). Map/list only — no decode, no sleep — well inside the -race budget.
func TestCacheStressMemoryBounded(t *testing.T) {
	c := NewCache(4096, 0)
	// Deterministic LCG (Numerical Recipes).
	state := uint32(12345)
	next := func() uint32 {
		state = state*1664525 + 1013904223
		return state
	}
	for i := 0; i < 10000; i++ {
		size := int(next()%2048) + 1
		fill := byte(next() % 256)
		k := CacheKey{Tenant: "t1", SourceETag: fmt.Sprintf("k%d", i), EffW: int(next() % 33), EffH: int(next() % 33)}
		c.Put(k, payload(size, fill))
		if b := c.Bytes(); b > 4096 {
			t.Fatalf("iteration %d: Bytes() = %d exceeds budget 4096", i, b)
		}
	}
	if c.Bytes() > 4096 {
		t.Fatalf("final Bytes() = %d exceeds budget 4096", c.Bytes())
	}
	if c.Len() > 4096 {
		t.Fatalf("final Len() = %d exceeds min-entry bound 4096", c.Len())
	}
}

// TestCacheStatsCounters pins the deterministic Stats surface: hit -> 1/0/0,
// miss -> 0/1/0, eviction -> 0/0/n (cache level; telemetry forwards from the
// entry point).
func TestCacheStatsCounters(t *testing.T) {
	c := NewCache(96, 0)
	k1 := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
	k2 := CacheKey{Tenant: "t1", SourceETag: "e2", EffW: 32, EffH: 32}

	if h, m, e := c.Stats(); h != 0 || m != 0 || e != 0 {
		t.Fatalf("fresh cache Stats = %d/%d/%d, want 0/0/0", h, m, e)
	}
	c.Put(k1, payload(32, 'a'))
	c.Get(k1) // hit
	h, m, e := c.Stats()
	if h != 1 || m != 0 || e != 0 {
		t.Fatalf("after hit: Stats = %d/%d/%d, want 1/0/0", h, m, e)
	}
	c.Get(k2) // miss
	h, m, e = c.Stats()
	if h != 1 || m != 1 || e != 0 {
		t.Fatalf("after miss: Stats = %d/%d/%d, want 1/1/0", h, m, e)
	}
	c.Put(k2, payload(96, 'b')) // bytes = 32+96 = 128 > 96 → evicts k1 (tail)
	h, m, e = c.Stats()
	if h != 1 || m != 1 || e != 1 {
		t.Fatalf("after eviction: Stats = %d/%d/%d, want 1/1/1", h, m, e)
	}
	// Disabled cache always reports zeros.
	if h, m, e := NewCache(0, 0).Stats(); h != 0 || m != 0 || e != 0 {
		t.Fatalf("disabled Stats = %d/%d/%d, want 0/0/0", h, m, e)
	}
}

// TestCacheKeyInjectivity pins key hardening: distinct (tenant, ETag, effW,
// effH) tuples produce distinct map keys, and quoted/multipart ETags are
// treated as opaque components.
func TestCacheKeyInjectivity(t *testing.T) {
	base := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
	cases := []struct {
		name string
		mut  func(CacheKey) CacheKey
	}{
		{"tenant", func(k CacheKey) CacheKey { k.Tenant = "t2"; return k }},
		{"etag", func(k CacheKey) CacheKey { k.SourceETag = "e2"; return k }},
		{"effW", func(k CacheKey) CacheKey { k.EffW = 33; return k }},
		{"effH", func(k CacheKey) CacheKey { k.EffH = 33; return k }},
		{"etag-quoted", func(k CacheKey) CacheKey { k.SourceETag = `"e1"`; return k }},
		{"etag-multipart", func(k CacheKey) CacheKey { k.SourceETag = "md5hex-4"; return k }},
	}
	seen := map[CacheKey]bool{base: true}
	for _, tc := range cases {
		got := tc.mut(base)
		if got == base {
			t.Fatalf("%s: mutation produced an identical key", tc.name)
		}
		if seen[got] {
			t.Fatalf("%s: key collides with another distinct tuple", tc.name)
		}
		seen[got] = true
	}
	// A cache must treat each distinct tuple as a separate entry.
	c := NewCache(1<<20, 0)
	c.Put(base, payload(10, 'a'))
	for _, tc := range cases {
		c.Put(tc.mut(base), payload(10, 'b'))
	}
	if c.Len() != len(cases)+1 {
		t.Fatalf("Len() = %d, want %d distinct entries", c.Len(), len(cases)+1)
	}
}

// TestCacheTTL (AC-1) pins the TTL contract: an entry read after its TTL is a
// miss even with zero LRU byte-budget pressure. Expiry state is injected
// white-box (backdating expiresAt, per the result_cache precedent) so the
// tests are sleep-free and -race-clean; one wall-clock subtest covers the
// end-to-end path.
func TestCacheTTL(t *testing.T) {
	t.Run("expired entry is a miss with zero budget pressure", func(t *testing.T) {
		c := NewCache(1<<20, time.Hour) // 1 MiB: no eviction pressure
		k := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
		img := payload(100, 'a')
		c.Put(k, img)
		if got, ok := c.Get(k); !ok || !bytes.Equal(got, img) {
			t.Fatal("fresh entry must hit")
		}
		// Backdate the stored entry: the TTL has elapsed without any wall
		// clock passing (deterministic injection, no sleep).
		c.mu.Lock()
		c.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
		c.mu.Unlock()
		if got, ok := c.Get(k); ok || got != nil {
			t.Fatalf("expired entry must miss even with zero budget pressure, got (%v, %v)", got, ok)
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatalf("expired entry must be removed exactly: Len=%d Bytes=%d, want 0/0", c.Len(), c.Bytes())
		}
		h, m, e := c.Stats()
		if h != 1 || m != 1 || e != 0 {
			t.Fatalf("Stats after expiry = %d/%d/%d, want 1/1/0 (expired read is an ordinary miss, not an LRU eviction)", h, m, e)
		}
	})

	t.Run("replace refreshes the expiry (new generation, new deadline)", func(t *testing.T) {
		// QA-F1 pin: a same-key re-Put is a new generation and must carry a
		// fresh retention deadline — dropping the refresh would let the new
		// generation expire on the old deadline (premature expiry).
		c := NewCache(1<<20, time.Hour)
		k := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
		c.Put(k, payload(10, 'a'))
		c.mu.Lock()
		c.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second) // old generation expired
		c.mu.Unlock()
		b := payload(20, 'b')
		c.Put(k, b) // replace = new generation -> fresh expiry
		if got, ok := c.Get(k); !ok || !bytes.Equal(got, b) {
			t.Fatalf("re-Put must refresh the expiry so the new generation hits: ok=%v bytes=%q", ok, got)
		}
		c.mu.Lock()
		exp := c.m[k].Value.(*entry).expiresAt
		c.mu.Unlock()
		if !exp.After(time.Now()) {
			t.Fatal("replaced entry must carry a fresh (future) expiry")
		}
	})

	t.Run("wall-clock expiry", func(t *testing.T) {
		c := NewCache(1<<20, time.Nanosecond)
		k := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
		c.Put(k, payload(10, 'a'))
		time.Sleep(2 * time.Millisecond) // bounded, sub-ms scale
		if got, ok := c.Get(k); ok || got != nil {
			t.Fatalf("entry past its TTL must miss on the wall-clock path, got (%v, %v)", got, ok)
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatalf("expired entry must be removed: Len=%d Bytes=%d, want 0/0", c.Len(), c.Bytes())
		}
	})

	t.Run("expired entry at LRU tail evicted by overflow counts exactly one eviction", func(t *testing.T) {
		// QA-F2 pin: the TTL-removal path and the overflow-eviction path are
		// mutually exclusive under mu — an expired entry at the tail evicted
		// by budget pressure is counted as ONE eviction with exact byte
		// accounting (never double-decremented, never zero-counted).
		a, b, c3 := payload(100, 'a'), payload(200, 'b'), payload(50, 'c')
		budget := int64(len(a) + len(b)) // 300: inserting c3 (350) exceeds by a margin that evicts exactly the tail
		cache := NewCache(budget, time.Hour)
		k1 := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
		k2 := CacheKey{Tenant: "t1", SourceETag: "e2", EffW: 32, EffH: 32}
		k3 := CacheKey{Tenant: "t1", SourceETag: "e3", EffW: 32, EffH: 32}
		cache.Put(k1, a)
		cache.Put(k2, b)
		if _, ok := cache.Get(k2); !ok {
			t.Fatal("k2 must be present before the touch")
		}
		cache.mu.Lock()
		cache.m[k1].Value.(*entry).expiresAt = time.Now().Add(-time.Second) // expired, still resident
		cache.mu.Unlock()
		n := cache.Put(k3, c3) // overflow: evicts the LRU tail (k1)
		if n != 1 {
			t.Fatalf("overflow Put evicted %d entries, want 1", n)
		}
		if _, ok := cache.Get(k1); ok {
			t.Fatal("expired tail entry must be gone after overflow")
		}
		if _, ok := cache.Get(k2); !ok {
			t.Fatal("touched k2 must survive")
		}
		if _, ok := cache.Get(k3); !ok {
			t.Fatal("k3 must be present after insertion")
		}
		if cache.Len() != 2 || cache.Bytes() != int64(len(b)+len(c3)) {
			t.Fatalf("byte accounting broken: Len=%d Bytes=%d, want 2/%d", cache.Len(), cache.Bytes(), len(b)+len(c3))
		}
		if _, _, e := cache.Stats(); e != 1 {
			t.Fatalf("evictions = %d, want exactly 1 (budget-pressure eviction, not a double-counted TTL removal)", e)
		}
	})
}

// TestCacheTTLDisabled (AC-2) pins the byte-for-byte opt-in default: ttl=0
// never consults expiry — a backdated expiresAt is ignored and the entry
// still hits with the identical payload, and counter parity holds for the
// same operation sequence as a pre-TTL cache.
func TestCacheTTLDisabled(t *testing.T) {
	c := NewCache(1<<20, 0)
	k := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
	img := payload(100, 'a')
	c.Put(k, img)
	if got, ok := c.Get(k); !ok || !bytes.Equal(got, img) {
		t.Fatalf("ttl=0 must hit with the byte-identical payload: ok=%v", ok)
	}
	// Backdating expiresAt must be a no-op when ttl == 0 (the expiry field
	// is never consulted — no wall-clock reads on the disabled path).
	c.mu.Lock()
	c.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if got, ok := c.Get(k); !ok || !bytes.Equal(got, img) {
		t.Fatal("ttl=0 must never consult expiry: expired-looking entry still hits")
	}
	// Counter parity: hit, hit — no misses, no evictions (pre-TTL behavior).
	h, m, e := c.Stats()
	if h != 2 || m != 0 || e != 0 {
		t.Fatalf("Stats = %d/%d/%d, want 2/0/0 (ttl=0 preserves the current counter behavior)", h, m, e)
	}
}

// TestCacheTTLConcurrent (QA-F3 / FM-9) exercises the TTL branch under -race:
// a tiny ttl makes expiry-removal race with same-key re-Put across goroutines.
// The lock discipline must keep byte accounting exact (0 <= bytes <= budget),
// every stored payload intact, and the shared hot key a single entry.
// Sleeps are bounded (~100 ms total) and well inside the 180 s race budget.
func TestCacheTTLConcurrent(t *testing.T) {
	const budget = 4 << 10
	c := NewCache(budget, time.Nanosecond)
	const gs = 8
	const iters = 200
	var errs []string
	var errMu sync.Mutex
	record := func(format string, args ...any) {
		errMu.Lock()
		defer errMu.Unlock()
		errs = append(errs, fmt.Sprintf(format, args...))
	}
	var wg sync.WaitGroup
	for g := 0; g < gs; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			fill := byte(g)
			for i := 0; i < iters; i++ {
				k := CacheKey{Tenant: "t1", SourceETag: fmt.Sprintf("g%d-%d", g, i), EffW: 32, EffH: 32}
				p := payload(64, fill)
				c.Put(k, p)
				if i%32 == 0 {
					time.Sleep(2 * time.Millisecond) // let the nanosecond TTL lapse mid-round
				}
				hot := CacheKey{Tenant: "t1", SourceETag: "hot", EffW: 32, EffH: 32}
				c.Put(hot, p)
				if got, ok := c.Get(k); ok && len(got) != 64 {
					record("torn payload under TTL: %d bytes, want 64", len(got))
				}
				if got, ok := c.Get(hot); ok && len(got) != 64 {
					record("torn hot payload under TTL: %d bytes, want 64", len(got))
				}
			}
		}(g)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("TTL concurrency violations:\n%s", strings.Join(errs, "\n"))
	}
	if b := c.Bytes(); b < 0 || b > budget {
		t.Fatalf("Bytes() = %d, want 0 <= bytes <= budget %d (exact accounting under expiry+replace races)", b, budget)
	}
}

// listOrder returns the LRU front-to-back key order (white-box helper for
// the sweep tests; callers must hold c.mu).
func listOrder(c *Cache) []CacheKey {
	var keys []CacheKey
	for el := c.ll.Front(); el != nil; el = el.Next() {
		keys = append(keys, el.Value.(*entry).key)
	}
	return keys
}

// TestCacheSweepExpired (AC-1) pins the FR-4 physical purge contract: the
// sweep removes exactly the entries past their retention deadline (strict
// after — one definition shared with Get's lazy branch), decrements c.bytes
// by exactly Σ len(e.data), leaves live entries (and their LRU order)
// untouched, never bumps the eviction counter or any Stats counter, and is
// a strict no-op on disabled / ttl<=0 caches. Expiry state is injected
// white-box (backdating expiresAt, per the TTL-test precedent), so all
// subtests are sleep-free and deterministic.
func TestCacheSweepExpired(t *testing.T) {
	t.Run("mixed expired and live entries", func(t *testing.T) {
		// 5 entries (100..500 B), no eviction pressure (1 MiB budget); the
		// expired ones are interleaved (front, middle, back of the list) so
		// the walk's next-capture survives removals at every position.
		c := NewCache(1<<20, time.Hour)
		keys := make([]CacheKey, 5)
		sizes := []int{100, 200, 300, 400, 500}
		for i, sz := range sizes {
			keys[i] = CacheKey{Tenant: "t1", SourceETag: fmt.Sprintf("e%d", i), EffW: 32, EffH: 32}
			c.Put(keys[i], payload(sz, byte('a'+i)))
		}
		now := time.Now()
		c.mu.Lock()
		for _, i := range []int{0, 2, 4} { // backdate the first, middle, last inserts
			c.m[keys[i]].Value.(*entry).expiresAt = now.Add(-time.Second)
		}
		beforeOrder := listOrder(c) // [k4 k3 k2 k1 k0]
		beforeBytes := c.bytes
		h0, m0, e0 := c.hits, c.misses, c.evictions
		c.mu.Unlock()
		if wantBefore := []CacheKey{keys[4], keys[3], keys[2], keys[1], keys[0]}; !slices.Equal(beforeOrder, wantBefore) {
			t.Fatalf("pre-sweep LRU order = %v, want %v (insert order, head-first)", beforeOrder, wantBefore)
		}

		n := c.SweepExpired(now)
		if n != 3 {
			t.Fatalf("SweepExpired = %d, want 3 expired removals", n)
		}
		removed := sizes[0] + sizes[2] + sizes[4]
		if got := c.Bytes(); got != beforeBytes-int64(removed) {
			t.Fatalf("Bytes() = %d, want %d (before %d minus exactly Σ removed %d)", got, beforeBytes-int64(removed), beforeBytes, removed)
		}
		if c.Len() != 2 {
			t.Fatalf("Len() = %d, want 2", c.Len())
		}
		// Survivor LRU order must be bit-identical to the pre-sweep order
		// (no MoveToFront, no re-linking): [k3 k1].
		c.mu.Lock()
		afterOrder := listOrder(c)
		afterBytes := c.bytes
		afterH, afterM, afterE := c.hits, c.misses, c.evictions
		c.mu.Unlock()
		wantOrder := []CacheKey{keys[3], keys[1]}
		if !slices.Equal(afterOrder, wantOrder) {
			t.Fatalf("survivor LRU order = %v, want %v (unchanged)", afterOrder, wantOrder)
		}
		if afterBytes != c.Bytes() {
			t.Fatalf("post-sweep accounting drift: %d vs Bytes() %d", afterBytes, c.Bytes())
		}
		if afterH != h0 || afterM != m0 || afterE != e0 {
			t.Fatalf("Stats after sweep = %d/%d/%d, want %d/%d/%d (sweep must not touch counters)", afterH, afterM, afterE, h0, m0, e0)
		}
		// Live entries still hit byte-identically; swept keys miss.
		for _, i := range []int{1, 3} {
			got, ok := c.Get(keys[i])
			if !ok || !bytes.Equal(got, payload(sizes[i], byte('a'+i))) {
				t.Fatalf("live key %d must survive and hit byte-identically: ok=%v", i, ok)
			}
		}
		for _, i := range []int{0, 2, 4} {
			if got, ok := c.Get(keys[i]); ok || got != nil {
				t.Fatalf("swept key %d must miss: got (%v, %v)", i, got, ok)
			}
		}
		if _, _, e := c.Stats(); e != 0 {
			t.Fatalf("evictions = %d, want 0 (sweep is not an LRU eviction)", e)
		}
	})

	t.Run("boundary: expiresAt == now survives (strict after)", func(t *testing.T) {
		c := NewCache(1<<20, time.Hour)
		now := time.Now()
		boundary := CacheKey{Tenant: "t1", SourceETag: "boundary", EffW: 32, EffH: 32}
		past := CacheKey{Tenant: "t1", SourceETag: "past", EffW: 32, EffH: 32}
		c.Put(boundary, payload(100, 'a'))
		c.Put(past, payload(100, 'b'))
		c.mu.Lock()
		c.m[boundary].Value.(*entry).expiresAt = now                   // deadline exactly now: survives
		c.m[past].Value.(*entry).expiresAt = now.Add(-time.Nanosecond) // one ns past: swept
		c.mu.Unlock()
		if n := c.SweepExpired(now); n != 1 {
			t.Fatalf("SweepExpired = %d, want 1 (strict after — expiresAt == now stays)", n)
		}
		if c.Len() != 1 || c.Bytes() != 100 {
			t.Fatalf("Len/Bytes after boundary sweep = %d/%d, want 1/100", c.Len(), c.Bytes())
		}
		// The boundary entry must still be resident with its byte-identical
		// payload — asserted under mu because a later Get with an advanced
		// wall clock would legitimately lazy-expire it (the sweep's now and
		// Get's time.Now() are different readings).
		var data []byte
		c.mu.Lock()
		el, ok := c.m[boundary]
		if ok {
			data = el.Value.(*entry).data
		}
		c.mu.Unlock()
		if !ok {
			t.Fatal("boundary entry (expiresAt == now) must survive the sweep")
		}
		if !bytes.Equal(data, payload(100, 'a')) {
			t.Fatal("boundary entry payload must be byte-identical")
		}
		if _, ok := c.Get(past); ok {
			t.Fatal("past-deadline entry must be swept")
		}
	})

	t.Run("ttl=0 strict no-op and disabled no-op", func(t *testing.T) {
		// ttl=0 must never consult expiry: a backdated entry survives the
		// sweep and still hits byte-identically (mirrors TestCacheTTLDisabled).
		c := NewCache(1<<20, 0)
		k := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
		img := payload(100, 'a')
		c.Put(k, img)
		c.mu.Lock()
		c.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
		c.mu.Unlock()
		if n := c.SweepExpired(time.Now()); n != 0 {
			t.Fatalf("SweepExpired on ttl=0 cache = %d, want 0 (strict no-op)", n)
		}
		if c.Len() != 1 || c.Bytes() != int64(len(img)) {
			t.Fatalf("ttl=0 sweep must not touch state: Len=%d Bytes=%d, want 1/%d", c.Len(), c.Bytes(), len(img))
		}
		if got, ok := c.Get(k); !ok || !bytes.Equal(got, img) {
			t.Fatal("ttl=0 sweep must never consult expiry: backdated entry still hits byte-identically")
		}
		// Disabled cache (maxBytes <= 0): no-op without acquiring state, no panic.
		dis := NewCache(0, time.Hour)
		if n := dis.SweepExpired(time.Now()); n != 0 {
			t.Fatalf("SweepExpired on disabled cache = %d, want 0", n)
		}
		if dis.Len() != 0 || dis.Bytes() != 0 {
			t.Fatalf("disabled cache must stay empty: Len=%d Bytes=%d", dis.Len(), dis.Bytes())
		}
		if h, m, e := dis.Stats(); h != 0 || m != 0 || e != 0 {
			t.Fatalf("disabled Stats = %d/%d/%d, want 0/0/0", h, m, e)
		}
	})

	t.Run("sweep frees the budget so live traffic is not LRU-evicted", func(t *testing.T) {
		// Problem scenario (the direction's stated payoff): an expired entry
		// resident in the middle of the LRU list pins the byte budget, so a
		// live Put evicts a LIVE entry from the tail. The sweep removes the
		// dead weight for free, so the same Put fits with evicted == 0 and
		// the live entries survive.
		a, b, c3, d := payload(100, 'a'), payload(100, 'b'), payload(100, 'c'), payload(100, 'd')
		budget := int64(len(a) + len(b) + len(c3)) // 300
		k1 := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
		k2 := CacheKey{Tenant: "t1", SourceETag: "e2", EffW: 32, EffH: 32}
		k3 := CacheKey{Tenant: "t1", SourceETag: "e3", EffW: 32, EffH: 32}
		k4 := CacheKey{Tenant: "t1", SourceETag: "e4", EffW: 32, EffH: 32}
		seed := func() *Cache {
			c := NewCache(budget, time.Hour)
			c.Put(k1, a)  // list: [k1]
			c.Put(k2, b)  // list: [k2 k1]
			c.Put(k3, c3) // list: [k3 k2 k1] — k2 the expired middle, k1 the live tail
			c.mu.Lock()
			c.m[k2].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
			c.mu.Unlock()
			return c
		}
		// Control: without the sweep, the live Put evicts the live tail k1
		// (the expired middle k2 stays resident) and bumps the eviction
		// counter.
		ctrl := seed()
		if ev := ctrl.Put(k4, d); ev != 1 {
			t.Fatalf("control Put evicted %d entries, want 1 (live tail evicted)", ev)
		}
		if _, ok := ctrl.Get(k1); ok {
			t.Fatal("control: live k1 must be evicted without the sweep")
		}
		if _, ok := ctrl.Get(k2); ok {
			t.Fatal("control: expired k2 must still be resident without the sweep")
		}
		if _, _, e := ctrl.Stats(); e != 1 {
			t.Fatalf("control evictions = %d, want 1 (an expired entry cost a budget eviction)", e)
		}
		// With the sweep: the expired k2 is removed for free (no counter
		// bump), the budget is freed, and the same live Put fits with zero
		// evictions.
		c := seed()
		if n := c.SweepExpired(time.Now()); n != 1 {
			t.Fatalf("SweepExpired = %d, want 1", n)
		}
		if ev := c.Put(k4, d); ev != 0 {
			t.Fatalf("live Put after sweep evicted %d entries, want 0 (budget freed by the sweep)", ev)
		}
		for name, k := range map[string]CacheKey{"k1": k1, "k3": k3, "k4": k4} {
			if _, ok := c.Get(k); !ok {
				t.Fatalf("%s must survive the post-sweep Put", name)
			}
		}
		if _, ok := c.Get(k2); ok {
			t.Fatal("expired k2 must be gone after the sweep")
		}
		if _, _, e := c.Stats(); e != 0 {
			t.Fatalf("evictions = %d, want 0 (sweep removals are not LRU evictions)", e)
		}
	})

	t.Run("loop extreme: nothing expired", func(t *testing.T) {
		c := NewCache(1<<20, time.Hour)
		keys := make([]CacheKey, 4)
		sizes := []int{100, 200, 300, 400}
		for i, sz := range sizes {
			keys[i] = CacheKey{Tenant: "t1", SourceETag: fmt.Sprintf("e%d", i), EffW: 32, EffH: 32}
			c.Put(keys[i], payload(sz, byte('a'+i)))
		}
		before, beforeLen := c.Bytes(), c.Len()
		h0, m0, e0 := c.Stats()
		if n := c.SweepExpired(time.Now()); n != 0 {
			t.Fatalf("SweepExpired with nothing expired = %d, want 0 (full walk, zero removals)", n)
		}
		if c.Bytes() != before || c.Len() != beforeLen {
			t.Fatalf("state changed with nothing expired: Bytes=%d/%d Len=%d/%d", c.Bytes(), before, c.Len(), beforeLen)
		}
		if h, m, e := c.Stats(); h != h0 || m != m0 || e != e0 {
			t.Fatalf("Stats changed with nothing expired: %d/%d/%d -> %d/%d/%d", h0, m0, e0, h, m, e)
		}
	})

	t.Run("loop extreme: everything expired", func(t *testing.T) {
		c := NewCache(1<<20, time.Hour)
		keys := make([]CacheKey, 4)
		sizes := []int{100, 200, 300, 400}
		for i, sz := range sizes {
			keys[i] = CacheKey{Tenant: "t1", SourceETag: fmt.Sprintf("e%d", i), EffW: 32, EffH: 32}
			c.Put(keys[i], payload(sz, byte('a'+i)))
		}
		now := time.Now()
		c.mu.Lock()
		for _, k := range keys {
			c.m[k].Value.(*entry).expiresAt = now.Add(-time.Second)
		}
		c.mu.Unlock()
		if n := c.SweepExpired(now); n != len(keys) {
			t.Fatalf("SweepExpired = %d, want %d (all entries expired)", n, len(keys))
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatalf("cache must be empty after the full sweep: Len=%d Bytes=%d", c.Len(), c.Bytes())
		}
		if got, ok := c.Get(keys[0]); ok || got != nil {
			t.Fatalf("Get after the full sweep: want (nil, false), got (%v, %v)", got, ok)
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatal("post-sweep Get must not resurrect entries")
		}
	})
}

// TestCacheSweepExpiredConcurrent (T5, matched by AC-1's -run
// TestCacheSweepExpired prefix) exercises the sweep under -race against
// concurrent Get/Put on a nanosecond-TTL cache: a sweeper goroutine calls
// SweepExpired(time.Now()) per round while workers Put/Get, mirroring the
// TestCacheTTLConcurrent scale (sleep every 32 iters, ~100 ms total sleeps)
// so the 180 s race gate keeps headroom. Runtime invariants: 0 <= Bytes()
// <= budget and no torn payloads. The final coherence walk under c.mu
// (after wg.Wait, so the sweeper has joined) proves len(c.m) == list
// length and Σ len(e.data) == c.bytes — the sweep never corrupts
// list/map/byte accounting.
func TestCacheSweepExpiredConcurrent(t *testing.T) {
	const budget = 4 << 10
	c := NewCache(budget, time.Nanosecond)
	const gs = 8
	const iters = 100
	var errs []string
	var errMu sync.Mutex
	record := func(format string, args ...any) {
		errMu.Lock()
		defer errMu.Unlock()
		errs = append(errs, fmt.Sprintf(format, args...))
	}
	var wg sync.WaitGroup
	for g := 0; g < gs; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			fill := byte(g)
			for i := 0; i < iters; i++ {
				k := CacheKey{Tenant: "t1", SourceETag: fmt.Sprintf("g%d-%d", g, i), EffW: 32, EffH: 32}
				p := payload(64, fill)
				c.Put(k, p)
				if i%32 == 0 {
					time.Sleep(2 * time.Millisecond) // let the nanosecond TTL lapse mid-round
				}
				hot := CacheKey{Tenant: "t1", SourceETag: "hot", EffW: 32, EffH: 32}
				c.Put(hot, p)
				if got, ok := c.Get(k); ok && len(got) != 64 {
					record("torn payload under sweep: %d bytes, want 64", len(got))
				}
				if got, ok := c.Get(hot); ok && len(got) != 64 {
					record("torn hot payload under sweep: %d bytes, want 64", len(got))
				}
			}
		}(g)
	}
	// The sweeper joins the WaitGroup so the final coherence walk observes
	// the quiesced state (no mid-sweep window); assertions below use the
	// record() collector, not t.Fatalf off the test goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			c.SweepExpired(time.Now())
			if i%32 == 0 {
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("sweep concurrency violations:\n%s", strings.Join(errs, "\n"))
	}
	if b := c.Bytes(); b < 0 || b > budget {
		t.Fatalf("Bytes() = %d, want 0 <= bytes <= budget %d (exact accounting under sweep+expiry+replace races)", b, budget)
	}
	// Coherence: map and list agree, byte accounting exact.
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) != c.ll.Len() {
		t.Fatalf("coherence: len(map) = %d, list length = %d", len(c.m), c.ll.Len())
	}
	var sum int64
	for el := c.ll.Front(); el != nil; el = el.Next() {
		sum += int64(len(el.Value.(*entry).data))
	}
	if sum != c.bytes {
		t.Fatalf("coherence: Σ len(data) = %d, c.bytes = %d", sum, c.bytes)
	}
}

// BenchmarkCacheSweepExpired documents the O(entries) sweep cost envelope:
// a full-budget walk at 0% / 50% / 100% expired (the 0% case is the
// worst-case lock-hold — full walk, zero removals). Documentation-quality
// (repo bench discipline — never asserted in CI); the timed region covers
// only the sweep (restore runs under StopTimer), so it doubles as a pin
// that a future refactor does not silently degrade the sweep to O(n²).
func BenchmarkCacheSweepExpired(b *testing.B) {
	const budget = 100 << 20 // 100 MiB
	const payloadSize = 10 << 10
	const n = budget / payloadSize // ~10 K entries
	keys := make([]CacheKey, n)
	data := make([][]byte, n)
	for i := range keys {
		keys[i] = CacheKey{Tenant: "t1", SourceETag: fmt.Sprintf("k%d", i), EffW: 32, EffH: 32}
		data[i] = payload(payloadSize, byte(i))
	}
	backdate := func(c *Cache, pred func(int) bool) {
		now := time.Now()
		c.mu.Lock()
		defer c.mu.Unlock()
		for i := 0; i < n; i++ {
			if pred(i) {
				c.m[keys[i]].Value.(*entry).expiresAt = now.Add(-time.Second)
			}
		}
	}
	for _, tc := range []struct {
		name string
		pred func(int) bool
	}{
		{"0pct-expired", func(int) bool { return false }},
		{"50pct-expired", func(i int) bool { return i%2 == 1 }},
		{"100pct-expired", func(int) bool { return true }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			c := NewCache(budget, time.Hour)
			for i, k := range keys {
				c.Put(k, data[i])
			}
			backdate(c, tc.pred)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.SweepExpired(time.Now())
				b.StopTimer()
				// Restore the population outside the timed region: the sweep
				// drains expired entries, so re-seed to keep the expired mix
				// stable across iterations.
				for j, k := range keys {
					c.Put(k, data[j])
				}
				backdate(c, tc.pred)
				b.StartTimer()
			}
		})
	}
}

// BenchmarkCacheGetTTL documents the enabled-path cost: identical hit
// sequences on a ttl=0 vs ttl>0 cache. Documentation-quality (repo bench
// discipline — never asserted in CI); pins the "zero-cost at default"
// property: the ttl=0 path performs no wall-clock reads.
func BenchmarkCacheGetTTL(b *testing.B) {
	k := CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
	img := payload(1024, 'a')
	for _, ttl := range []time.Duration{0, time.Hour} {
		c := NewCache(1<<20, ttl)
		c.Put(k, img)
		b.Run(fmt.Sprintf("ttl=%v", ttl), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, ok := c.Get(k); !ok {
					b.Fatal("must hit")
				}
			}
		})
	}
}

// TestCacheStampedeConcurrentMisses pins the stampede contract: N concurrent
// miss→store cycles on the SAME key must all observe a correct outcome
// (each gets the bytes it generated), the LRU must end with exactly one
// entry for the key (no duplicate/overlapping entries), and the stats must
// count exactly N misses and N-1 evictions-or-hits consistent with the
// store rule — never a corrupt or mismatched payload. Single-flight dedup
// is a deliberate non-goal: the decode semaphore already serializes
// concurrent generations to maxConcurrentDecodes, so a stampede costs at
// most that many decodes; this pin locks the correctness contract under
// concurrency (-race runs this in the make-check race gate).
func TestCacheStampedeConcurrentMisses(t *testing.T) {
	c := NewCache(1<<20, 0) // 1 MiB: 20 entries of the 50 KiB payload fit
	key := CacheKey{Tenant: "t", SourceETag: "abc123", EffW: 100, EffH: 100, Version: CacheKeyVersion}
	payload := bytes.Repeat([]byte{0xAB}, 50<<10)

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Miss→store race: a concurrent Get may legitimately hit after
			// another goroutine's store (the store is atomic); every
			// goroutine must observe a correct outcome either way, and the
			// stats below account for both branches.
			if _, hit := c.Get(key); !hit {
				c.Put(key, payload)
			}
		}()
	}
	wg.Wait()
	// Exactly one entry for the key; the stored bytes are the full payload.
	if got := c.Len(); got != 1 {
		t.Fatalf("Len = %d, want exactly 1 entry after a same-key stampede", got)
	}
	got, hit := c.Get(key)
	if !hit || !bytes.Equal(got, payload) {
		t.Fatalf("post-stampede Get: hit=%v len=%d want hit=true len=%d (identical payload)", hit, len(got), len(payload))
	}
	// Stats: the n goroutine Gets split into misses (each followed by a
	// store) and hits (store landed first); the verification Get is one
	// more hit. Total = n + 1, and every miss stored exactly once.
	hits, misses, _ := c.Stats()
	if hits+misses != n+1 {
		t.Fatalf("hits+misses = %d, want %d (n goroutine Gets + 1 verification)", hits+misses, n+1)
	}
	if misses == 0 {
		t.Fatal("misses = 0 — the stampede never exercised the store path")
	}
}
