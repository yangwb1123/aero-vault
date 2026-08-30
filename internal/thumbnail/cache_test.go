package thumbnail

import (
	"bytes"
	"fmt"
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
	k1 := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
	k2 := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e2", EffW: 32, EffH: 32}

	// 1. Put then hit: same bytes, exact budget accounting, single entry.
	a := payload(100, 'a')
	c.Put(k1, a)
	got, outcome := c.Get(k1)
	if outcome != GetHit {
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
	if got, outcome := c.Get(k2); outcome != GetMiss || got != nil {
		t.Fatalf("Get(k2): want (nil, GetMiss), got (%v, %v)", got, outcome)
	}
	dis := NewCache(0, 0)
	if got, outcome := dis.Get(k1); outcome != GetMiss || got != nil {
		t.Fatalf("disabled Get: want (nil, GetMiss), got (%v, %v)", got, outcome)
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
	got, outcome = c.Get(k1)
	if outcome != GetHit || !bytes.Equal(got, b) {
		t.Fatalf("Get(k1) after replace: want (b, GetHit), got outcome=%v bytes=%q", outcome, got)
	}
	if c.Bytes() != int64(len(b)) {
		t.Fatalf("Bytes() after replace = %d, want %d", c.Bytes(), len(b))
	}

	// 4. Empty payload is a strict no-op (existing entry untouched).
	c.Put(k1, nil)
	if got, outcome := c.Get(k1); outcome != GetHit || !bytes.Equal(got, b) {
		t.Fatalf("empty Put must not disturb the existing entry: outcome=%v bytes=%q", outcome, got)
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
	k1 := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
	k2 := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e2", EffW: 32, EffH: 32}
	k3 := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e3", EffW: 32, EffH: 32}

	cache.Put(k1, a)
	cache.Put(k2, b)
	if cache.Bytes() != budget {
		t.Fatalf("Bytes() = %d, want exact budget %d", cache.Bytes(), budget)
	}
	if _, outcome := cache.Get(k1); outcome != GetHit {
		t.Fatal("k1 must be present at exact budget")
	}
	if _, outcome := cache.Get(k2); outcome != GetHit {
		t.Fatal("k2 must be present at exact budget")
	}

	// Inserting k3 overflows: the least-recently-used tail (k1) is evicted.
	n := cache.Put(k3, c)
	if n != 1 {
		t.Fatalf("Put(k3) evicted %d entries, want 1", n)
	}
	if _, outcome := cache.Get(k1); outcome == GetHit {
		t.Fatal("k1 must be evicted (oldest)")
	}
	if _, outcome := cache.Get(k2); outcome != GetHit {
		t.Fatal("k2 must survive eviction")
	}
	if _, outcome := cache.Get(k3); outcome != GetHit {
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
	if _, outcome := touched.Get(k2); outcome != GetHit {
		t.Fatal("k2 must be present before touch")
	}
	n = touched.Put(k3, c)
	if n != 1 {
		t.Fatalf("Put(k3) after touch evicted %d entries, want 1", n)
	}
	if _, outcome := touched.Get(k1); outcome == GetHit {
		t.Fatal("untouched k1 must be the LRU victim (recency-touch)")
	}
	if _, outcome := touched.Get(k2); outcome != GetHit {
		t.Fatal("touched k2 must survive eviction")
	}
	if _, outcome := touched.Get(k3); outcome != GetHit {
		t.Fatal("k3 must be present after insertion")
	}

	// Oversized entry: never stored, and no eviction counter increment.

	// Oversized entry (new key): never stored, and no eviction counter
	// increment.
	huge := payload(int(budget)+1, 'x')
	kBig := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "big", EffW: 32, EffH: 32}
	before := cache.Len()
	n = cache.Put(kBig, huge)
	if n != 0 {
		t.Fatalf("oversized Put must return 0 evictions, got %d", n)
	}
	if cache.Len() != before {
		t.Fatalf("oversized Put changed Len: %d -> %d", before, cache.Len())
	}
	if _, outcome := cache.Get(kBig); outcome == GetHit {
		t.Fatal("oversized payload must not be stored under kBig")
	}

	// Oversized replace of an existing key removes the superseded entry
	// (its payload must not be served as current) without counting it as a
	// budget-pressure eviction.
	cache.Put(k3, c)
	if _, outcome := cache.Get(k3); outcome != GetHit {
		t.Fatal("k3 must be present before oversized replace")
	}
	h, m, ev, _ := cache.Stats()
	cache.Put(k3, huge)
	if _, outcome := cache.Get(k3); outcome == GetHit {
		t.Fatal("superseded k3 must be removed by oversized replace")
	}
	_, _, ev2, _ := cache.Stats()
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
				k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: fmt.Sprintf("g%d-%d", g, i), EffW: 32, EffH: 32}
				p := payload(64, fill)
				c.Put(k, p)
				hot := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "hot", EffW: 32, EffH: 32}
				c.Put(hot, p)
				if got, outcome := c.Get(k); outcome == GetHit {
					// Payloads are never mutated after storage: any hit must
					// return a full-length, intact slice.
					if len(got) != 64 {
						record("torn payload: got %d bytes, want 64", len(got))
					}
				}
				if got, outcome := c.Get(hot); outcome == GetHit && len(got) != 64 {
					record("torn hot payload: got %d bytes, want 64", len(got))
				}
				if _, outcome := c.Get(CacheKey{Identity: SourceIdentity{TenantID: "t2", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "foreign", EffW: 32, EffH: 32}); outcome == GetHit {
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
	if got, outcome := c.Get(CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "hot", EffW: 32, EffH: 32}); outcome != GetHit || len(got) != 64 {
		t.Fatalf("hot key: want full payload, got outcome=%v len=%d", outcome, len(got))
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
		k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: fmt.Sprintf("k%d", i), EffW: int(next() % 33), EffH: int(next() % 33)}
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
	k1 := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
	k2 := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e2", EffW: 32, EffH: 32}

	if h, m, e, x := c.Stats(); h != 0 || m != 0 || e != 0 || x != 0 {
		t.Fatalf("fresh cache Stats = %d/%d/%d/%d, want 0/0/0/0", h, m, e, x)
	}
	c.Put(k1, payload(32, 'a'))
	c.Get(k1) // hit
	h, m, e, x := c.Stats()
	if h != 1 || m != 0 || e != 0 || x != 0 {
		t.Fatalf("after hit: Stats = %d/%d/%d/%d, want 1/0/0/0", h, m, e, x)
	}
	c.Get(k2) // miss
	h, m, e, x = c.Stats()
	if h != 1 || m != 1 || e != 0 || x != 0 {
		t.Fatalf("after miss: Stats = %d/%d/%d/%d, want 1/1/0/0", h, m, e, x)
	}
	c.Put(k2, payload(96, 'b')) // bytes = 32+96 = 128 > 96 → evicts k1 (tail)
	h, m, e, x = c.Stats()
	if h != 1 || m != 1 || e != 1 || x != 0 {
		t.Fatalf("after eviction: Stats = %d/%d/%d/%d, want 1/1/1/0", h, m, e, x)
	}
	// Disabled cache always reports zeros.
	if h, m, e, x := NewCache(0, 0).Stats(); h != 0 || m != 0 || e != 0 || x != 0 {
		t.Fatalf("disabled Stats = %d/%d/%d/%d, want 0/0/0/0", h, m, e, x)
	}
}

// TestCacheKeyInjectivity pins key hardening: every source-identity field,
// source ETag, effective dimension, and schema-version field participates in
// the comparable cache key. ETag spelling remains opaque at this layer.
func TestCacheKeyInjectivity(t *testing.T) {
	base := currentThumbnailCacheKey(
		SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"},
		"e1", 32, 32,
	)
	cases := []struct {
		name string
		mut  func(CacheKey) CacheKey
	}{
		{"tenant", func(k CacheKey) CacheKey { k.Identity.TenantID = "t2"; return k }},
		{"bucket", func(k CacheKey) CacheKey { k.Identity.Bucket = "other-bucket"; return k }},
		{"key", func(k CacheKey) CacheKey { k.Identity.Key = "other-key"; return k }},
		{"version-id", func(k CacheKey) CacheKey { k.Identity.VersionID = "other-version"; return k }},
		{"etag", func(k CacheKey) CacheKey { k.SourceETag = "e2"; return k }},
		{"effW", func(k CacheKey) CacheKey { k.EffW = 33; return k }},
		{"effH", func(k CacheKey) CacheKey { k.EffH = 33; return k }},
		{"version", func(k CacheKey) CacheKey { k.Version = CacheKeyVersion + 1; return k }},
		{"representation", func(k CacheKey) CacheKey {
			k.Representation = representationTokenForJPEGQuality(alternateJPEGQuality())
			return k
		}},
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

// TestCacheExpiredOutcome (AC-1) pins the accounting split introduced by the
// hit-ratio fix: a TTL-expired read is its OWN outcome class — removed, not
// served, counted in Cache.expired (telemetry: thumbnail.cache.expired_total)
// and contributing 0 to the hit-ratio miss count. The no-fire property for
// ThumbnailCacheHitRatioLow follows directly: an all-expired workload reads
// hits=0 ∧ misses=0, so the alert's activity guard (hits+misses rate > 0) is
// false and the alert cannot fire on a healthy sparse-TTL cache. Expiry state
// is injected white-box (backdating expiresAt per the TestCacheTTL precedent)
// so the test is sleep-free and -race clean.
func TestCacheExpiredOutcome(t *testing.T) {
	// 1. Fresh entry must hit with byte-identical payload.
	c := NewCache(1<<20, time.Hour)
	k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
	k2 := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e2", EffW: 32, EffH: 32}
	img := payload(100, 'a')
	c.Put(k, img)
	if got, outcome := c.Get(k); outcome != GetHit || !bytes.Equal(got, img) {
		t.Fatalf("fresh entry must hit: outcome=%v bytes_equal=%v", outcome, bytes.Equal(got, img))
	}

	// 2. Backdate the entry: the TTL has elapsed. The read must report
	// GetExpired, return nil, and remove the entry exactly.
	c.mu.Lock()
	c.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
	c.mu.Unlock()
	got, outcome := c.Get(k)
	if outcome != GetExpired || got != nil {
		t.Fatalf("expired read: want (nil, GetExpired), got (%v, %v)", got, outcome)
	}
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatalf("expired entry must be removed exactly: Len=%d Bytes=%d, want 0/0", c.Len(), c.Bytes())
	}

	// 3. An absent key is a genuine miss (GetMiss).
	if got, outcome := c.Get(k2); outcome != GetMiss || got != nil {
		t.Fatalf("absent key: want (nil, GetMiss), got (%v, %v)", got, outcome)
	}

	// 4. The expired read fed neither hits nor misses: h=1 (the fresh hit),
	// m=1 (the absent-key miss), e=0 (not an LRU eviction), x=1 (expired
	// class). If the expired read had been counted as a miss the hit-ratio
	// denominator would inflate and ThumbnailCacheHitRatioLow could fire on
	// a healthy sparse-TTL cache; it contributes 0 to both, so an all-
	// expired workload reads 0/0 for hits/misses and the activity guard
	// keeps the alert silent.
	h, m, e, x := c.Stats()
	if h != 1 || m != 1 || e != 0 || x != 1 {
		t.Fatalf("Stats = %d/%d/%d/%d, want 1/1/0/1 (expired read must not feed the hit-ratio miss count)", h, m, e, x)
	}

	// 5. Disabled cache (maxBytes <= 0) and ttl=0 cache never return
	// GetExpired — no wall-clock read on those paths, expired stays 0.
	dis := NewCache(0, time.Hour)
	if _, outcome := dis.Get(k); outcome != GetMiss {
		t.Fatalf("disabled Get: want GetMiss, got %v", outcome)
	}
	if _, _, _, x := dis.Stats(); x != 0 {
		t.Fatalf("disabled cache expired = %d, want 0 (no wall-clock read on the disabled path)", x)
	}
	noTTL := NewCache(1<<20, 0)
	noTTL.Put(k, img)
	noTTL.mu.Lock()
	noTTL.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second) // expired-looking, but ttl==0 never consults it
	noTTL.mu.Unlock()
	if _, outcome := noTTL.Get(k); outcome != GetHit {
		t.Fatalf("ttl=0 cache must ignore the backdated expiry and hit, got %v", outcome)
	}
	if _, _, _, x := noTTL.Stats(); x != 0 {
		t.Fatalf("ttl=0 cache expired = %d, want 0 (GetExpired unreachable without ttl > 0)", x)
	}
}
