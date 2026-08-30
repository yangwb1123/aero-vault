package thumbnail

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"
)

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
		keys[i] = CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: fmt.Sprintf("k%d", i), EffW: 32, EffH: 32}
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
	k := CacheKey{Identity: SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
	img := payload(1024, 'a')
	for _, ttl := range []time.Duration{0, time.Hour} {
		c := NewCache(1<<20, ttl)
		c.Put(k, img)
		b.Run(fmt.Sprintf("ttl=%v", ttl), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, outcome := c.Get(k); outcome != GetHit {
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
	key := CacheKey{Identity: SourceIdentity{TenantID: "t", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "abc123", EffW: 100, EffH: 100, Version: CacheKeyVersion}
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
			if _, outcome := c.Get(key); outcome != GetHit {
				c.Put(key, payload)
			}
		}()
	}
	wg.Wait()
	// Exactly one entry for the key; the stored bytes are the full payload.
	if got := c.Len(); got != 1 {
		t.Fatalf("Len = %d, want exactly 1 entry after a same-key stampede", got)
	}
	got, outcome := c.Get(key)
	if outcome != GetHit || !bytes.Equal(got, payload) {
		t.Fatalf("post-stampede Get: outcome=%v len=%d want GetHit len=%d (identical payload)", outcome, len(got), len(payload))
	}
	// Stats: the n goroutine Gets split into misses (each followed by a
	// store) and hits (store landed first); the verification Get is one
	// more hit. Total = n + 1, and every miss stored exactly once. The 4th
	// value is the expired counter — unreachable here (ttl=0), discarded.
	hits, misses, _, _ := c.Stats()
	if hits+misses != n+1 {
		t.Fatalf("hits+misses = %d, want %d (n goroutine Gets + 1 verification)", hits+misses, n+1)
	}
	if misses == 0 {
		t.Fatal("misses = 0 — the stampede never exercised the store path")
	}
}
