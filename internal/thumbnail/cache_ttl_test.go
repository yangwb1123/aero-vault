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

// TestCacheTTL (AC-1) pins the TTL contract: an entry read after its TTL is
// removed and reported as GetExpired — its own outcome class, distinct from a
// genuine miss (which feeds the hit-ratio count) and from an LRU eviction —
// even with zero LRU byte-budget pressure. Expiry state is injected
// white-box (backdating expiresAt, per the result_cache precedent) so the
// tests are sleep-free and -race-clean; one wall-clock subtest covers the
// end-to-end path.
func TestCacheTTL(t *testing.T) {
	t.Run("expired entry is its own class (expired), not a miss and not an LRU eviction", func(t *testing.T) {
		c := NewCache(1<<20, time.Hour) // 1 MiB: no eviction pressure
		k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
		img := payload(100, 'a')
		c.Put(k, img)
		if got, outcome := c.Get(k); outcome != GetHit || !bytes.Equal(got, img) {
			t.Fatal("fresh entry must hit")
		}
		// Backdate the stored entry: the TTL has elapsed without any wall
		// clock passing (deterministic injection, no sleep).
		c.mu.Lock()
		c.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
		c.mu.Unlock()
		if got, outcome := c.Get(k); outcome != GetExpired || got != nil {
			t.Fatalf("expired entry must report GetExpired with nil bytes, got (%v, %v)", got, outcome)
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatalf("expired entry must be removed exactly: Len=%d Bytes=%d, want 0/0", c.Len(), c.Bytes())
		}
		h, m, e, x := c.Stats()
		if h != 1 || m != 0 || e != 0 || x != 1 {
			t.Fatalf("Stats after expiry = %d/%d/%d/%d, want 1/0/0/1 (expired read is its own class — it must not feed the hit-ratio miss count)", h, m, e, x)
		}
	})

	t.Run("replace refreshes the expiry (new generation, new deadline)", func(t *testing.T) {
		// QA-F1 pin: a same-key re-Put is a new generation and must carry a
		// fresh retention deadline — dropping the refresh would let the new
		// generation expire on the old deadline (premature expiry).
		c := NewCache(1<<20, time.Hour)
		k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
		c.Put(k, payload(10, 'a'))
		c.mu.Lock()
		c.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second) // old generation expired
		c.mu.Unlock()
		b := payload(20, 'b')
		c.Put(k, b) // replace = new generation -> fresh expiry
		if got, outcome := c.Get(k); outcome != GetHit || !bytes.Equal(got, b) {
			t.Fatalf("re-Put must refresh the expiry so the new generation hits: outcome=%v bytes=%q", outcome, got)
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
		k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
		c.Put(k, payload(10, 'a'))
		time.Sleep(2 * time.Millisecond) // bounded, sub-ms scale
		if got, outcome := c.Get(k); outcome != GetExpired || got != nil {
			t.Fatalf("entry past its TTL must report GetExpired on the wall-clock path, got (%v, %v)", got, outcome)
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatalf("expired entry must be removed: Len=%d Bytes=%d, want 0/0", c.Len(), c.Bytes())
		}
	})

	t.Run("overflow reclaims expired bytes before live tail eviction fallback", func(t *testing.T) {
		// QA-F2 pin: reclaiming an expired tail entry is free (not an
		// eviction), but if the reclaimed bytes still do not fit the new
		// payload Put must fall back to exactly one live tail eviction.
		a, b, c3, d := payload(40, 'a'), payload(140, 'b'), payload(120, 'c'), payload(100, 'd')
		cache := NewCache(int64(len(a)+len(b)+len(c3)), time.Hour)
		k1, k2, k3, k4 := cacheKeyForETag("e1"), cacheKeyForETag("e2"), cacheKeyForETag("e3"), cacheKeyForETag("e4")
		cache.Put(k1, a)
		cache.Put(k2, b)
		cache.Put(k3, c3)
		cache.mu.Lock()
		cache.m[k1].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
		cache.mu.Unlock()
		h0, m0, e0, x0 := cache.Stats()
		if n := cache.Put(k4, d); n != 1 {
			t.Fatalf("overflow Put evicted %d entries, want 1 live fallback eviction", n)
		}
		if h, m, e, x := cache.Stats(); h != h0 || m != m0 || e != e0+1 || x != x0 {
			t.Fatalf("Stats after partial reclaim = %d/%d/%d/%d, want %d/%d/%d/%d", h, m, e, x, h0, m0, e0+1, x0)
		}
		if cache.Len() != 2 || cache.Bytes() != int64(len(c3)+len(d)) {
			t.Fatalf("Len/Bytes = %d/%d, want 2/%d", cache.Len(), cache.Bytes(), len(c3)+len(d))
		}
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if _, ok := cache.m[k1]; ok {
			t.Fatal("expired tail entry must be reclaimed before live eviction")
		}
		if _, ok := cache.m[k2]; ok {
			t.Fatal("live fallback eviction must remove k2 after reclaiming k1")
		}
		for name, tc := range map[string]struct {
			key  CacheKey
			want []byte
		}{
			"k3": {key: k3, want: c3},
			"k4": {key: k4, want: d},
		} {
			el, ok := cache.m[tc.key]
			if !ok {
				t.Fatalf("%s missing after partial reclaim fallback", name)
			}
			if got := el.Value.(*entry).data; !bytes.Equal(got, tc.want) {
				t.Fatalf("%s payload changed: got %q want %q", name, got, tc.want)
			}
		}
		if order := listOrder(cache); !slices.Equal(order, []CacheKey{k4, k3}) {
			t.Fatalf("LRU order = %v, want [%v %v]", order, k4, k3)
		}
	})
}

// TestCacheTTLDisabled (AC-2) pins the byte-for-byte opt-in default: ttl=0
// never consults expiry — a backdated expiresAt is ignored and the entry
// still hits with the identical payload, and counter parity holds for the
// same operation sequence as a pre-TTL cache.
func TestCacheTTLDisabled(t *testing.T) {
	c := NewCache(1<<20, 0)
	k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
	img := payload(100, 'a')
	c.Put(k, img)
	if got, outcome := c.Get(k); outcome != GetHit || !bytes.Equal(got, img) {
		t.Fatalf("ttl=0 must hit with the byte-identical payload: outcome=%v", outcome)
	}
	// Backdating expiresAt must be a no-op when ttl == 0 (the expiry field
	// is never consulted — no wall-clock reads on the disabled path).
	c.mu.Lock()
	c.m[k].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if got, outcome := c.Get(k); outcome != GetHit || !bytes.Equal(got, img) {
		t.Fatalf("ttl=0 must never consult expiry: expired-looking entry still hits (outcome=%v)", outcome)
	}
	// Counter parity: hit, hit — no misses, no evictions, no expired (pre-TTL
	// behavior: a ttl<=0 cache can never yield GetExpired).
	h, m, e, x := c.Stats()
	if h != 2 || m != 0 || e != 0 || x != 0 {
		t.Fatalf("Stats = %d/%d/%d/%d, want 2/0/0/0 (ttl=0 preserves the current counter behavior)", h, m, e, x)
	}
	// Disabled cache (maxBytes <= 0): every Get is a GetMiss and Stats stays
	// 0/0/0/0 — no wall-clock read, no expired class possible.
	dis := NewCache(0, time.Hour)
	if got, outcome := dis.Get(k); outcome != GetMiss || got != nil {
		t.Fatalf("disabled Get: want (nil, GetMiss), got (%v, %v)", got, outcome)
	}
	h, m, e, x = dis.Stats()
	if h != 0 || m != 0 || e != 0 || x != 0 {
		t.Fatalf("disabled Stats = %d/%d/%d/%d, want 0/0/0/0", h, m, e, x)
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
				k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: fmt.Sprintf("g%d-%d", g, i), EffW: 32, EffH: 32}
				p := payload(64, fill)
				c.Put(k, p)
				if i%32 == 0 {
					time.Sleep(2 * time.Millisecond) // let the nanosecond TTL lapse mid-round
				}
				hot := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "hot", EffW: 32, EffH: 32}
				c.Put(hot, p)
				if got, outcome := c.Get(k); outcome == GetHit && len(got) != 64 {
					record("torn payload under TTL: %d bytes, want 64", len(got))
				}
				if got, outcome := c.Get(hot); outcome == GetHit && len(got) != 64 {
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
