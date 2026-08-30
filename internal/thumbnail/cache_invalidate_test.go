package thumbnail

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

func invalidateCacheKey(id SourceIdentity, etag string, w, h int, version uint8, representation string) CacheKey {
	return CacheKey{
		Identity:       id,
		SourceETag:     etag,
		EffW:           w,
		EffH:           h,
		Version:        version,
		Representation: representation,
	}
}

func invalidateStats(c *Cache) [4]uint64 {
	h, m, e, x := c.Stats()
	return [4]uint64{h, m, e, x}
}

func filterInvalidateOrder(order []CacheKey, id SourceIdentity) []CacheKey {
	filtered := make([]CacheKey, 0, len(order))
	for _, key := range order {
		if !key.Identity.Equal(id) {
			filtered = append(filtered, key)
		}
	}
	return filtered
}

func assertInvalidateCacheInvariants(t *testing.T, c *Cache) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bytes < 0 || c.bytes > c.maxBytes {
		t.Fatalf("bytes=%d, want within [0,%d]", c.bytes, c.maxBytes)
	}
	if len(c.m) != c.ll.Len() {
		t.Fatalf("map/list length mismatch: map=%d list=%d", len(c.m), c.ll.Len())
	}
	seen := make(map[CacheKey]bool, len(c.m))
	var sum int64
	for el := c.ll.Front(); el != nil; el = el.Next() {
		e := el.Value.(*entry)
		sum += int64(len(e.data))
		if seen[e.key] {
			t.Fatalf("duplicate key in LRU list: %+v", e.key)
		}
		seen[e.key] = true
		if c.m[e.key] != el {
			t.Fatalf("map entry does not point at list element for %+v", e.key)
		}
	}
	if sum != c.bytes {
		t.Fatalf("bytes drift: sum=%d cache=%d", sum, c.bytes)
	}
	if len(seen) != len(c.m) {
		t.Fatalf("seen/list mismatch: seen=%d map=%d", len(seen), len(c.m))
	}
	for key, el := range c.m {
		if el == nil {
			t.Fatalf("nil list element stored for %+v", key)
		}
		if !seen[key] {
			t.Fatalf("map key missing from LRU list: %+v", key)
		}
	}
}

func TestCacheInvalidateSource(t *testing.T) {
	t.Run("removes all variants for one complete identity", func(t *testing.T) {
		cache := NewCache(1<<20, time.Hour)
		target := SourceIdentity{TenantID: "tenant-a", Bucket: "bucket-a", Key: "image.jpg", VersionID: "version-a"}
		altRep := representationTokenForJPEGQuality(alternateJPEGQuality())
		targetEntries := []struct {
			key  CacheKey
			data []byte
		}{
			{invalidateCacheKey(target, etagA, 32, 32, CacheKeyVersion, currentRepresentationToken), payload(101, 'a')},
			{invalidateCacheKey(target, etagB, 64, 32, CacheKeyVersion+1, currentRepresentationToken), payload(55, 'b')},
			{invalidateCacheKey(target, etagA, 32, 64, CacheKeyVersion, altRep), payload(77, 'c')},
			{invalidateCacheKey(target, etagB, 96, 48, CacheKeyVersion+2, altRep), payload(33, 'd')},
		}
		mismatchCases := []struct {
			name     string
			identity SourceIdentity
			key      CacheKey
			data     []byte
		}{
			{
				name:     "tenant mismatch survivor",
				identity: SourceIdentity{TenantID: "tenant-b", Bucket: target.Bucket, Key: target.Key, VersionID: target.VersionID},
				key:      invalidateCacheKey(SourceIdentity{TenantID: "tenant-b", Bucket: target.Bucket, Key: target.Key, VersionID: target.VersionID}, etagA, 32, 32, CacheKeyVersion, currentRepresentationToken),
				data:     payload(91, 't'),
			},
			{
				name:     "bucket mismatch survivor",
				identity: SourceIdentity{TenantID: target.TenantID, Bucket: "bucket-b", Key: target.Key, VersionID: target.VersionID},
				key:      invalidateCacheKey(SourceIdentity{TenantID: target.TenantID, Bucket: "bucket-b", Key: target.Key, VersionID: target.VersionID}, etagA, 32, 32, CacheKeyVersion, currentRepresentationToken),
				data:     payload(81, 'u'),
			},
			{
				name:     "key mismatch survivor",
				identity: SourceIdentity{TenantID: target.TenantID, Bucket: target.Bucket, Key: "image-2.jpg", VersionID: target.VersionID},
				key:      invalidateCacheKey(SourceIdentity{TenantID: target.TenantID, Bucket: target.Bucket, Key: "image-2.jpg", VersionID: target.VersionID}, etagA, 32, 32, CacheKeyVersion, currentRepresentationToken),
				data:     payload(71, 'v'),
			},
			{
				name:     "version mismatch survivor",
				identity: SourceIdentity{TenantID: target.TenantID, Bucket: target.Bucket, Key: target.Key, VersionID: "version-b"},
				key:      invalidateCacheKey(SourceIdentity{TenantID: target.TenantID, Bucket: target.Bucket, Key: target.Key, VersionID: "version-b"}, etagA, 32, 32, CacheKeyVersion, currentRepresentationToken),
				data:     payload(61, 'w'),
			},
		}
		inserts := []struct {
			key  CacheKey
			data []byte
		}{
			{mismatchCases[0].key, mismatchCases[0].data},
			{targetEntries[0].key, targetEntries[0].data},
			{targetEntries[1].key, targetEntries[1].data},
			{mismatchCases[1].key, mismatchCases[1].data},
			{targetEntries[2].key, targetEntries[2].data},
			{mismatchCases[2].key, mismatchCases[2].data},
			{targetEntries[3].key, targetEntries[3].data},
			{mismatchCases[3].key, mismatchCases[3].data},
		}
		for _, tc := range inserts {
			cache.Put(tc.key, tc.data)
		}
		nearMisses := []struct {
			name string
			id   SourceIdentity
		}{
			{"tenant near miss", SourceIdentity{TenantID: "tenant-miss", Bucket: target.Bucket, Key: target.Key, VersionID: target.VersionID}},
			{"bucket near miss", SourceIdentity{TenantID: target.TenantID, Bucket: "bucket-miss", Key: target.Key, VersionID: target.VersionID}},
			{"key near miss", SourceIdentity{TenantID: target.TenantID, Bucket: target.Bucket, Key: "image-miss.jpg", VersionID: target.VersionID}},
			{"version near miss", SourceIdentity{TenantID: target.TenantID, Bucket: target.Bucket, Key: target.Key, VersionID: "version-miss"}},
		}
		cache.mu.Lock()
		cache.m[targetEntries[1].key].Value.(*entry).expiresAt = time.Unix(1, 0)
		noOpOrder := listOrder(cache)
		cache.mu.Unlock()
		noOpStats := invalidateStats(cache)
		noOpLen, noOpBytes := cache.Len(), cache.Bytes()
		for _, tc := range nearMisses {
			if removed := cache.InvalidateSource(tc.id); removed != 0 {
				t.Fatalf("%s removed=%d, want 0", tc.name, removed)
			}
			cache.mu.Lock()
			el, ok := cache.m[targetEntries[0].key]
			cache.mu.Unlock()
			if !ok || !bytes.Equal(el.Value.(*entry).data, targetEntries[0].data) {
				t.Fatalf("%s disturbed the target entry", tc.name)
			}
		}
		cache.mu.Lock()
		afterNoOpOrder := listOrder(cache)
		cache.mu.Unlock()
		if cache.Len() != noOpLen || cache.Bytes() != noOpBytes {
			t.Fatalf("no-op invalidation changed Len/Bytes: %d/%d -> %d/%d", noOpLen, noOpBytes, cache.Len(), cache.Bytes())
		}
		if got := invalidateStats(cache); got != noOpStats {
			t.Fatalf("no-op invalidation changed Stats: got=%v want=%v", got, noOpStats)
		}
		if !slices.Equal(afterNoOpOrder, noOpOrder) {
			t.Fatalf("no-op invalidation changed LRU order: before=%v after=%v", noOpOrder, afterNoOpOrder)
		}

		beforeStats := invalidateStats(cache)
		beforeLen, beforeBytes := cache.Len(), cache.Bytes()
		cache.mu.Lock()
		beforeOrder := listOrder(cache)
		cache.mu.Unlock()
		removedBytes := int64(0)
		for _, tc := range targetEntries {
			removedBytes += int64(len(tc.data))
		}
		removed := cache.InvalidateSource(target)
		if removed != len(targetEntries) {
			t.Fatalf("InvalidateSource removed=%d, want %d", removed, len(targetEntries))
		}
		if cache.Len() != beforeLen-len(targetEntries) {
			t.Fatalf("Len()=%d, want %d", cache.Len(), beforeLen-len(targetEntries))
		}
		if cache.Bytes() != beforeBytes-removedBytes {
			t.Fatalf("Bytes()=%d, want %d", cache.Bytes(), beforeBytes-removedBytes)
		}
		if got := invalidateStats(cache); got != beforeStats {
			t.Fatalf("InvalidateSource changed Stats: got=%v want=%v", got, beforeStats)
		}
		cache.mu.Lock()
		afterOrder := listOrder(cache)
		cache.mu.Unlock()
		if want := filterInvalidateOrder(beforeOrder, target); !slices.Equal(afterOrder, want) {
			t.Fatalf("survivor LRU order=%v, want %v", afterOrder, want)
		}
		for _, tc := range targetEntries {
			if got, outcome := cache.Get(tc.key); got != nil || outcome != GetMiss {
				t.Fatalf("target key outcome=%v len=%d, want GetMiss/nil", outcome, len(got))
			}
		}
		for _, tc := range mismatchCases {
			got, outcome := cache.Get(tc.key)
			if outcome != GetHit || !bytes.Equal(got, tc.data) {
				t.Fatalf("survivor %s outcome=%v sameBytes=%v", tc.name, outcome, bytes.Equal(got, tc.data))
			}
		}
	})

	t.Run("incomplete identity is a strict no-op", func(t *testing.T) {
		cache := NewCache(1<<20, time.Hour)
		key := currentThumbnailCacheKey(testIdentity("tenant-a"), etagA, 32, 32)
		img := payload(64, 'n')
		cache.Put(key, img)
		beforeStats := invalidateStats(cache)
		beforeLen, beforeBytes := cache.Len(), cache.Bytes()
		cache.mu.Lock()
		beforeOrder := listOrder(cache)
		cache.mu.Unlock()
		if removed := cache.InvalidateSource(SourceIdentity{TenantID: "tenant-a", Bucket: "bucket", Key: "image"}); removed != 0 {
			t.Fatalf("incomplete identity removed=%d, want 0", removed)
		}
		cache.mu.Lock()
		afterOrder := listOrder(cache)
		cache.mu.Unlock()
		if cache.Len() != beforeLen || cache.Bytes() != beforeBytes {
			t.Fatalf("incomplete identity changed Len/Bytes: %d/%d -> %d/%d", beforeLen, beforeBytes, cache.Len(), cache.Bytes())
		}
		if got := invalidateStats(cache); got != beforeStats {
			t.Fatalf("incomplete identity changed Stats: got=%v want=%v", got, beforeStats)
		}
		if !slices.Equal(afterOrder, beforeOrder) {
			t.Fatalf("incomplete identity changed LRU order: before=%v after=%v", beforeOrder, afterOrder)
		}
		if got, outcome := cache.Get(key); outcome != GetHit || !bytes.Equal(got, img) {
			t.Fatalf("incomplete identity disturbed resident entry: outcome=%v sameBytes=%v", outcome, bytes.Equal(got, img))
		}
	})

	t.Run("disabled cache is a strict no-op", func(t *testing.T) {
		cache := NewCache(0, time.Hour)
		if removed := cache.InvalidateSource(testIdentity("tenant-a")); removed != 0 {
			t.Fatalf("disabled cache removed=%d, want 0", removed)
		}
		if cache.Len() != 0 || cache.Bytes() != 0 {
			t.Fatalf("disabled cache state changed: Len=%d Bytes=%d", cache.Len(), cache.Bytes())
		}
		if got := invalidateStats(cache); got != [4]uint64{} {
			t.Fatalf("disabled cache Stats=%v, want zero", got)
		}
	})
}

func TestCacheInvalidateKeepsStats(t *testing.T) {
	cache := NewCache(400, time.Hour)
	victimKey := currentThumbnailCacheKey(SourceIdentity{TenantID: "tenant-a", Bucket: "bucket", Key: "victim", VersionID: "v1"}, etagA, 32, 32)
	hitKey := currentThumbnailCacheKey(SourceIdentity{TenantID: "tenant-a", Bucket: "bucket", Key: "hit", VersionID: "v1"}, etagB, 32, 32)
	evictKey := currentThumbnailCacheKey(SourceIdentity{TenantID: "tenant-a", Bucket: "bucket", Key: "evict", VersionID: "v1"}, etagA, 48, 48)
	expiredKey := currentThumbnailCacheKey(SourceIdentity{TenantID: "tenant-a", Bucket: "bucket", Key: "expired", VersionID: "v1"}, etagB, 48, 48)
	cache.Put(victimKey, payload(200, 'v'))
	cache.Put(hitKey, payload(150, 'h'))
	if evicted := cache.Put(evictKey, payload(150, 'e')); evicted != 1 {
		t.Fatalf("eviction setup evicted=%d, want 1", evicted)
	}
	if _, outcome := cache.Get(hitKey); outcome != GetHit {
		t.Fatalf("hit setup outcome=%v, want GetHit", outcome)
	}
	missKey := currentThumbnailCacheKey(SourceIdentity{TenantID: "tenant-a", Bucket: "bucket", Key: "miss", VersionID: "v1"}, etagA, 64, 64)
	if _, outcome := cache.Get(missKey); outcome != GetMiss {
		t.Fatalf("miss setup outcome=%v, want GetMiss", outcome)
	}
	expiredData := payload(50, 'x')
	cache.Put(expiredKey, expiredData)
	cache.mu.Lock()
	cache.m[expiredKey].Value.(*entry).expiresAt = time.Unix(1, 0)
	cache.mu.Unlock()
	if _, outcome := cache.Get(expiredKey); outcome != GetExpired {
		t.Fatalf("expired setup outcome=%v, want GetExpired", outcome)
	}
	statsBefore := invalidateStats(cache)
	if statsBefore != [4]uint64{1, 1, 1, 1} {
		t.Fatalf("setup Stats=%v, want [1 1 1 1]", statsBefore)
	}

	target := SourceIdentity{TenantID: "tenant-a", Bucket: "bucket", Key: "invalidate", VersionID: "v1"}
	altRep := representationTokenForJPEGQuality(alternateJPEGQuality())
	targetEntries := []struct {
		key  CacheKey
		data []byte
	}{
		{invalidateCacheKey(target, etagA, 32, 32, CacheKeyVersion, currentRepresentationToken), payload(20, 'i')},
		{invalidateCacheKey(target, etagB, 64, 64, CacheKeyVersion+1, altRep), payload(30, 'j')},
	}
	for _, tc := range targetEntries {
		cache.Put(tc.key, tc.data)
	}
	beforeLen, beforeBytes := cache.Len(), cache.Bytes()
	removedBytes := int64(len(targetEntries[0].data) + len(targetEntries[1].data))
	if removed := cache.InvalidateSource(target); removed != len(targetEntries) {
		t.Fatalf("InvalidateSource removed=%d, want %d", removed, len(targetEntries))
	}
	if got := invalidateStats(cache); got != statsBefore {
		t.Fatalf("InvalidateSource changed Stats: got=%v want=%v", got, statsBefore)
	}
	if cache.Len() != beforeLen-len(targetEntries) {
		t.Fatalf("Len()=%d, want %d", cache.Len(), beforeLen-len(targetEntries))
	}
	if cache.Bytes() != beforeBytes-removedBytes {
		t.Fatalf("Bytes()=%d, want %d", cache.Bytes(), beforeBytes-removedBytes)
	}
}

func TestCacheInvalidateSourceConcurrent(t *testing.T) {
	cache := NewCache(8<<10, time.Hour)
	identities := []SourceIdentity{
		{TenantID: "tenant-a", Bucket: "bucket", Key: "a", VersionID: "v1"},
		{TenantID: "tenant-a", Bucket: "bucket", Key: "b", VersionID: "v1"},
		{TenantID: "tenant-b", Bucket: "bucket", Key: "a", VersionID: "v2"},
	}
	altRep := representationTokenForJPEGQuality(alternateJPEGQuality())
	keys := []CacheKey{
		invalidateCacheKey(identities[0], etagA, 32, 32, CacheKeyVersion, currentRepresentationToken),
		invalidateCacheKey(identities[0], etagB, 48, 48, CacheKeyVersion+1, altRep),
		invalidateCacheKey(identities[1], etagA, 64, 64, CacheKeyVersion, currentRepresentationToken),
		invalidateCacheKey(identities[1], etagB, 96, 48, CacheKeyVersion+1, altRep),
		invalidateCacheKey(identities[2], etagA, 32, 64, CacheKeyVersion, currentRepresentationToken),
		invalidateCacheKey(identities[2], etagB, 64, 32, CacheKeyVersion+1, altRep),
	}
	for i, key := range keys {
		cache.Put(key, payload(64+i*3, byte('a'+i)))
	}
	const iters = 300
	start := make(chan struct{})
	var wg sync.WaitGroup
	run := func(fn func(int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				fn(i)
			}
		}()
	}
	run(func(i int) {
		key := keys[i%len(keys)]
		cache.Put(key, payload(32+(i%5)*7, byte('k'+i%11)))
	})
	run(func(i int) {
		_, _ = cache.Get(keys[(i*3)%len(keys)])
	})
	run(func(i int) {
		cache.mu.Lock()
		idx := 0
		for el := cache.ll.Front(); el != nil; el = el.Next() {
			if idx%2 == i%2 {
				el.Value.(*entry).expiresAt = time.Unix(1, 0)
			}
			idx++
		}
		cache.mu.Unlock()
		cache.SweepExpired(time.Unix(2, 0))
	})
	run(func(i int) {
		cache.InvalidateSource(identities[i%len(identities)])
	})
	close(start)
	wg.Wait()
	assertInvalidateCacheInvariants(t, cache)
}

func TestCacheInvalidateSourceFlightIsolation(t *testing.T) {
	cache := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	staleKey := invalidateCacheKey(identity, etagB, 48, 48, CacheKeyVersion+1, representationTokenForJPEGQuality(alternateJPEGQuality()))
	cache.Put(staleKey, []byte("stale"))
	opener := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	type result struct {
		img       []byte
		fromCache bool
		err       error
	}
	start := make(chan struct{})
	done := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			img, fromCache, err := GenerateContextWithOpenerCached(context.Background(), cache, identity, etagA, 32, 32, opener.open)
			done <- result{img: img, fromCache: fromCache, err: err}
		}()
	}
	close(start)
	waitFlightEntered(t, opener)
	flightKey := currentThumbnailCacheKey(identity, etagA, 32, 32)
	waitFlightJoiners(t, cache, flightKey, 1)
	if removed := cache.InvalidateSource(identity); removed != 1 {
		t.Fatalf("InvalidateSource during flight removed=%d, want 1 stale resident entry", removed)
	}
	cache.mu.Lock()
	_, stalePresent := cache.m[staleKey]
	cache.mu.Unlock()
	if stalePresent {
		t.Fatal("stale resident entry survived invalidation")
	}
	close(opener.release)
	var first []byte
	fromCacheCount := 0
	for i := 0; i < 2; i++ {
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("flight caller %d err=%v", i, got.err)
			}
			if first == nil {
				first = got.img
			} else if !bytes.Equal(first, got.img) {
				t.Fatalf("flight caller %d received different bytes", i)
			}
			if got.fromCache {
				fromCacheCount++
			}
		case <-time.After(2 * time.Second):
			t.Fatal("flight caller did not finish after invalidation")
		}
	}
	if opener.opens.Load() != 1 {
		t.Fatalf("opener calls=%d, want 1", opener.opens.Load())
	}
	if fromCacheCount != 1 {
		t.Fatalf("joined caller count=%d, want 1", fromCacheCount)
	}
	assertFlightGone(t, cache, flightKey)
	if got, fromCache, err := GenerateContextWithOpenerCached(context.Background(), cache, identity, etagA, 32, 32, opener.open); err != nil || !fromCache || !bytes.Equal(got, first) {
		t.Fatalf("follow-up cached hit err=%v fromCache=%v sameBytes=%v", err, fromCache, bytes.Equal(got, first))
	}
	if cache.Len() != 1 {
		t.Fatalf("cache entries=%d, want 1 repopulated entry", cache.Len())
	}
}
