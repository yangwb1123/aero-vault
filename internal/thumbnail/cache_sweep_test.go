package thumbnail

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

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
			keys[i] = CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: fmt.Sprintf("e%d", i), EffW: 32, EffH: 32}
			c.Put(keys[i], payload(sz, byte('a'+i)))
		}
		now := time.Now()
		c.mu.Lock()
		for _, i := range []int{0, 2, 4} { // backdate the first, middle, last inserts
			c.m[keys[i]].Value.(*entry).expiresAt = now.Add(-time.Second)
		}
		beforeOrder := listOrder(c) // [k4 k3 k2 k1 k0]
		beforeBytes := c.bytes
		h0, m0, e0, x0 := c.hits, c.misses, c.evictions, c.expired
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
		afterH, afterM, afterE, afterX := c.hits, c.misses, c.evictions, c.expired
		c.mu.Unlock()
		wantOrder := []CacheKey{keys[3], keys[1]}
		if !slices.Equal(afterOrder, wantOrder) {
			t.Fatalf("survivor LRU order = %v, want %v (unchanged)", afterOrder, wantOrder)
		}
		if afterBytes != c.Bytes() {
			t.Fatalf("post-sweep accounting drift: %d vs Bytes() %d", afterBytes, c.Bytes())
		}
		if afterH != h0 || afterM != m0 || afterE != e0 || afterX != x0 {
			t.Fatalf("Stats after sweep = %d/%d/%d/%d, want %d/%d/%d/%d (sweep must not touch counters)", afterH, afterM, afterE, afterX, h0, m0, e0, x0)
		}
		// Live entries still hit byte-identically; swept keys miss.
		for _, i := range []int{1, 3} {
			got, outcome := c.Get(keys[i])
			if outcome != GetHit || !bytes.Equal(got, payload(sizes[i], byte('a'+i))) {
				t.Fatalf("live key %d must survive and hit byte-identically: outcome=%v", i, outcome)
			}
		}
		for _, i := range []int{0, 2, 4} {
			if got, outcome := c.Get(keys[i]); outcome == GetHit || got != nil {
				t.Fatalf("swept key %d must miss: got (%v, %v)", i, got, outcome)
			}
		}
		if _, _, e, _ := c.Stats(); e != 0 {
			t.Fatalf("evictions = %d, want 0 (sweep is not an LRU eviction)", e)
		}
	})

	t.Run("boundary: expiresAt == now survives (strict after)", func(t *testing.T) {
		c := NewCache(1<<20, time.Hour)
		now := time.Now()
		boundary := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "boundary", EffW: 32, EffH: 32}
		past := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "past", EffW: 32, EffH: 32}
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
		if _, outcome := c.Get(past); outcome == GetHit {
			t.Fatal("past-deadline entry must be swept")
		}
	})

	t.Run("ttl=0 strict no-op and disabled no-op", func(t *testing.T) {
		// ttl=0 must never consult expiry: a backdated entry survives the
		// sweep and still hits byte-identically (mirrors TestCacheTTLDisabled).
		c := NewCache(1<<20, 0)
		k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
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
		if got, outcome := c.Get(k); outcome != GetHit || !bytes.Equal(got, img) {
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
		if h, m, e, x := dis.Stats(); h != 0 || m != 0 || e != 0 || x != 0 {
			t.Fatalf("disabled Stats = %d/%d/%d/%d, want 0/0/0/0", h, m, e, x)
		}
	})

	t.Run("direct Put reclaim matches explicit prior sweep", func(t *testing.T) {
		// The explicit sweep remains valid, but it is no longer required for
		// correctness: a reclaim-only overflow Put must converge to the same
		// resident state and counter accounting as a prior SweepExpired.
		a, b, c3, d := payload(100, 'a'), payload(100, 'b'), payload(100, 'c'), payload(100, 'd')
		budget := int64(len(a) + len(b) + len(c3))
		k1, k2, k3, k4 := cacheKeyForETag("e1"), cacheKeyForETag("e2"), cacheKeyForETag("e3"), cacheKeyForETag("e4")
		seed := func() *Cache {
			c := NewCache(budget, time.Hour)
			c.Put(k1, a)
			c.Put(k2, b)
			c.Put(k3, c3)
			c.mu.Lock()
			c.m[k2].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
			c.mu.Unlock()
			return c
		}
		assertState := func(t *testing.T, c *Cache) {
			t.Helper()
			if c.Len() != 3 || c.Bytes() != budget {
				t.Fatalf("Len/Bytes = %d/%d, want 3/%d", c.Len(), c.Bytes(), budget)
			}
			if h, m, e, x := c.Stats(); h != 0 || m != 0 || e != 0 || x != 0 {
				t.Fatalf("Stats = %d/%d/%d/%d, want 0/0/0/0", h, m, e, x)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if _, ok := c.m[k2]; ok {
				t.Fatal("expired middle entry must be reclaimed")
			}
			for name, tc := range map[string]struct {
				key  CacheKey
				want []byte
			}{
				"k1": {key: k1, want: a},
				"k3": {key: k3, want: c3},
				"k4": {key: k4, want: d},
			} {
				el, ok := c.m[tc.key]
				if !ok {
					t.Fatalf("%s missing after reclaim-only Put", name)
				}
				if got := el.Value.(*entry).data; !bytes.Equal(got, tc.want) {
					t.Fatalf("%s payload changed: got %q want %q", name, got, tc.want)
				}
			}
			if order := listOrder(c); !slices.Equal(order, []CacheKey{k4, k3, k1}) {
				t.Fatalf("LRU order = %v, want [%v %v %v]", order, k4, k3, k1)
			}
		}

		direct := seed()
		if ev := direct.Put(k4, d); ev != 0 {
			t.Fatalf("direct Put evicted %d entries, want 0", ev)
		}
		assertState(t, direct)

		swept := seed()
		if n := swept.SweepExpired(time.Now()); n != 1 {
			t.Fatalf("SweepExpired = %d, want 1", n)
		}
		if ev := swept.Put(k4, d); ev != 0 {
			t.Fatalf("post-sweep Put evicted %d entries, want 0", ev)
		}
		assertState(t, swept)
	})

	t.Run("loop extreme: nothing expired", func(t *testing.T) {
		c := NewCache(1<<20, time.Hour)
		keys := make([]CacheKey, 4)
		sizes := []int{100, 200, 300, 400}
		for i, sz := range sizes {
			keys[i] = CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: fmt.Sprintf("e%d", i), EffW: 32, EffH: 32}
			c.Put(keys[i], payload(sz, byte('a'+i)))
		}
		before, beforeLen := c.Bytes(), c.Len()
		h0, m0, e0, x0 := c.Stats()
		if n := c.SweepExpired(time.Now()); n != 0 {
			t.Fatalf("SweepExpired with nothing expired = %d, want 0 (full walk, zero removals)", n)
		}
		if c.Bytes() != before || c.Len() != beforeLen {
			t.Fatalf("state changed with nothing expired: Bytes=%d/%d Len=%d/%d", c.Bytes(), before, c.Len(), beforeLen)
		}
		if h, m, e, x := c.Stats(); h != h0 || m != m0 || e != e0 || x != x0 {
			t.Fatalf("Stats changed with nothing expired: %d/%d/%d/%d -> %d/%d/%d/%d", h0, m0, e0, x0, h, m, e, x)
		}
	})

	t.Run("loop extreme: everything expired", func(t *testing.T) {
		c := NewCache(1<<20, time.Hour)
		keys := make([]CacheKey, 4)
		sizes := []int{100, 200, 300, 400}
		for i, sz := range sizes {
			keys[i] = CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: fmt.Sprintf("e%d", i), EffW: 32, EffH: 32}
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
		if got, outcome := c.Get(keys[0]); outcome != GetMiss || got != nil {
			t.Fatalf("Get after the full sweep: want (nil, GetMiss), got (%v, %v)", got, outcome)
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
				k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: fmt.Sprintf("g%d-%d", g, i), EffW: 32, EffH: 32}
				p := payload(64, fill)
				c.Put(k, p)
				if i%32 == 0 {
					time.Sleep(2 * time.Millisecond) // let the nanosecond TTL lapse mid-round
				}
				hot := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "hot", EffW: 32, EffH: 32}
				c.Put(hot, p)
				if got, outcome := c.Get(k); outcome == GetHit && len(got) != 64 {
					record("torn payload under sweep: %d bytes, want 64", len(got))
				}
				if got, outcome := c.Get(hot); outcome == GetHit && len(got) != 64 {
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
