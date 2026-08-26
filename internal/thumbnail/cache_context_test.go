package thumbnail

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

type cacheLookupResult struct {
	data    []byte
	outcome GetOutcome
	err     error
}

func waitCacheLookupSignal(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}

func waitCacheLookupResult(t *testing.T, ch <-chan cacheLookupResult, msg string) cacheLookupResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
	return cacheLookupResult{}
}

func TestCacheGetContextCanceledWaiterMissPreservesMiss(t *testing.T) {
	c := NewCache(1<<20, 0)
	liveKey := CacheKey{
		Identity:   SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "live", VersionID: "version"},
		SourceETag: etagA,
		EffW:       32,
		EffH:       32,
		Version:    CacheKeyVersion,
	}
	missKey := liveKey
	missKey.Identity.Key = "miss"
	missKey.SourceETag = etagB
	c.Put(liveKey, payload(64, 'a'))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.mu.Lock()
	locked := true
	defer func() {
		if locked {
			c.mu.Unlock()
		}
	}()
	beforeOrder := listOrder(c)
	beforeHits, beforeMisses, beforeEvictions, beforeExpired := c.hits, c.misses, c.evictions, c.expired
	done := make(chan cacheLookupResult, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		data, outcome, err := c.getContext(ctx, missKey)
		done <- cacheLookupResult{data: data, outcome: outcome, err: err}
	}()
	waitCacheLookupSignal(t, started, "miss lookup did not start while cache lock was held")
	cancel()
	c.mu.Unlock()
	locked = false

	result := waitCacheLookupResult(t, done, "miss lookup did not finish")
	if result.err != nil {
		t.Fatalf("miss lookup err = %v, want nil", result.err)
	}
	if result.outcome != GetMiss || result.data != nil {
		t.Fatalf("miss lookup = (%v, %v), want (nil, GetMiss)", result.data, result.outcome)
	}

	c.mu.Lock()
	afterOrder := listOrder(c)
	afterHits, afterMisses, afterEvictions, afterExpired := c.hits, c.misses, c.evictions, c.expired
	c.mu.Unlock()
	if afterHits != beforeHits {
		t.Fatalf("miss lookup changed hits: before=%d after=%d", beforeHits, afterHits)
	}
	if afterMisses != beforeMisses+1 {
		t.Fatalf("miss lookup misses=%d, want %d", afterMisses, beforeMisses+1)
	}
	if afterEvictions != beforeEvictions {
		t.Fatalf("miss lookup changed evictions: before=%d after=%d", beforeEvictions, afterEvictions)
	}
	if afterExpired != beforeExpired {
		t.Fatalf("miss lookup changed expired count: before=%d after=%d", beforeExpired, afterExpired)
	}
	if !slices.Equal(afterOrder, beforeOrder) {
		t.Fatalf("miss lookup changed LRU order: before=%v after=%v", beforeOrder, afterOrder)
	}
}

func TestCacheGetContextCanceledWaiterExpiredPreservesExpired(t *testing.T) {
	c := NewCache(1<<20, time.Hour)
	expiredKey := CacheKey{
		Identity:   SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "expired", VersionID: "version"},
		SourceETag: etagA,
		EffW:       32,
		EffH:       32,
		Version:    CacheKeyVersion,
	}
	liveKey := expiredKey
	liveKey.Identity.Key = "live"
	liveKey.SourceETag = etagB
	expiredData := payload(48, 'e')
	liveData := payload(96, 'l')
	c.Put(expiredKey, expiredData)
	c.Put(liveKey, liveData)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.mu.Lock()
	locked := true
	defer func() {
		if locked {
			c.mu.Unlock()
		}
	}()
	c.m[expiredKey].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
	beforeBytes := c.bytes
	beforeHits, beforeMisses, beforeEvictions, beforeExpired := c.hits, c.misses, c.evictions, c.expired
	done := make(chan cacheLookupResult, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		data, outcome, err := c.getContext(ctx, expiredKey)
		done <- cacheLookupResult{data: data, outcome: outcome, err: err}
	}()
	waitCacheLookupSignal(t, started, "expired lookup did not start while cache lock was held")
	cancel()
	c.mu.Unlock()
	locked = false

	result := waitCacheLookupResult(t, done, "expired lookup did not finish")
	if result.err != nil {
		t.Fatalf("expired lookup err = %v, want nil", result.err)
	}
	if result.outcome != GetExpired || result.data != nil {
		t.Fatalf("expired lookup = (%v, %v), want (nil, GetExpired)", result.data, result.outcome)
	}

	c.mu.Lock()
	afterOrder := listOrder(c)
	afterBytes := c.bytes
	afterHits, afterMisses, afterEvictions, afterExpired := c.hits, c.misses, c.evictions, c.expired
	_, expiredStillPresent := c.m[expiredKey]
	c.mu.Unlock()
	if afterHits != beforeHits {
		t.Fatalf("expired lookup changed hits: before=%d after=%d", beforeHits, afterHits)
	}
	if afterMisses != beforeMisses {
		t.Fatalf("expired lookup changed misses: before=%d after=%d", beforeMisses, afterMisses)
	}
	if afterEvictions != beforeEvictions {
		t.Fatalf("expired lookup changed evictions: before=%d after=%d", beforeEvictions, afterEvictions)
	}
	if afterExpired != beforeExpired+1 {
		t.Fatalf("expired lookup expired=%d, want %d", afterExpired, beforeExpired+1)
	}
	if afterBytes != beforeBytes-int64(len(expiredData)) {
		t.Fatalf("expired lookup bytes=%d, want %d", afterBytes, beforeBytes-int64(len(expiredData)))
	}
	if expiredStillPresent {
		t.Fatal("expired lookup left the expired entry in the cache")
	}
	if !slices.Equal(afterOrder, []CacheKey{liveKey}) {
		t.Fatalf("expired lookup order=%v, want [%v]", afterOrder, liveKey)
	}
}

// BenchmarkGenerateContextWithOpenerCachedHitParallel establishes a warmed,
// contended cached-hit baseline. It should stay zero-alloc on hits and never
// re-open after the initial warm-up miss.
func BenchmarkGenerateContextWithOpenerCachedHitParallel(b *testing.B) {
	c := NewCache(1<<20, 0)
	fixture := benchFixture(b, 256, 256)
	var opens atomic.Int64
	ctx := context.Background()
	open := countingOpener3(fixture, etagA, &opens)
	if _, _, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 128, 128, open); err != nil {
		b.Fatal(err)
	}
	if opens.Load() != 1 {
		b.Fatalf("warm-up opens = %d, want 1", opens.Load())
	}
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	errCh := make(chan string, 1)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 128, 128, open); err != nil {
				select {
				case errCh <- err.Error():
				default:
				}
				return
			} else if !from {
				select {
				case errCh <- "cached hit benchmark observed a miss":
				default:
				}
				return
			}
		}
	})
	select {
	case msg := <-errCh:
		b.Fatal(msg)
	default:
	}
	if opens.Load() != 1 {
		b.Fatalf("hit benchmark reopened source: opens=%d, want 1", opens.Load())
	}
}
