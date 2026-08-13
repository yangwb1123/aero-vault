package thumbnail

// White-box tests for the bounded byte-budget LRU cache (cache.go). All
// fixtures are deterministic (byte slices / LCG); no sleeps; -short and
// -race friendly within the 120 s test-race-thumbnail budget.

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
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
	c := NewCache(1 << 20)
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
	dis := NewCache(0)
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
	cache := NewCache(budget)
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
	touched := NewCache(budget)
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
	c := NewCache(budget)
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
	c := NewCache(4096)
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
	c := NewCache(96)
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
	if h, m, e := NewCache(0).Stats(); h != 0 || m != 0 || e != 0 {
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
	c := NewCache(1 << 20)
	c.Put(base, payload(10, 'a'))
	for _, tc := range cases {
		c.Put(tc.mut(base), payload(10, 'b'))
	}
	if c.Len() != len(cases)+1 {
		t.Fatalf("Len() = %d, want %d distinct entries", c.Len(), len(cases)+1)
	}
}
