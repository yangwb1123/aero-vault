package thumbnail

import (
	"bytes"
	"slices"
	"testing"
	"time"
)

func cacheKeyForETag(etag string) CacheKey {
	return CacheKey{
		Identity:   SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"},
		SourceETag: etag,
		EffW:       32,
		EffH:       32,
	}
}

func TestCachePutReclaimsExpiredBeforeLiveEviction(t *testing.T) {
	c := NewCache(300, time.Hour)
	k1, k2, k3, k4 := cacheKeyForETag("e1"), cacheKeyForETag("e2"), cacheKeyForETag("e3"), cacheKeyForETag("e4")
	a, b, c3, d := payload(100, 'a'), payload(100, 'b'), payload(100, 'c'), payload(100, 'd')
	c.Put(k1, a)
	c.Put(k2, b)
	c.Put(k3, c3)

	c.mu.Lock()
	c.m[k2].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
	c.mu.Unlock()

	beforeBytes, beforeLen := c.Bytes(), c.Len()
	h0, m0, e0, x0 := c.Stats()
	if ev := c.Put(k4, d); ev != 0 {
		t.Fatalf("Put(k4) evicted %d entries, want 0 after reclaiming expired bytes", ev)
	}
	if c.Len() != beforeLen {
		t.Fatalf("Len() = %d, want %d", c.Len(), beforeLen)
	}
	if c.Bytes() != beforeBytes {
		t.Fatalf("Bytes() = %d, want %d", c.Bytes(), beforeBytes)
	}
	if h, m, e, x := c.Stats(); h != h0 || m != m0 || e != e0 || x != x0 {
		t.Fatalf("Stats after reclaim = %d/%d/%d/%d, want %d/%d/%d/%d", h, m, e, x, h0, m0, e0, x0)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k2]; ok {
		t.Fatal("expired middle entry must be reclaimed before live eviction")
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
			t.Fatalf("%s missing after reclaim-only overflow Put", name)
		}
		if got := el.Value.(*entry).data; !bytes.Equal(got, tc.want) {
			t.Fatalf("%s payload changed: got %q want %q", name, got, tc.want)
		}
	}
	if order := listOrder(c); !slices.Equal(order, []CacheKey{k4, k3, k1}) {
		t.Fatalf("LRU order = %v, want [%v %v %v]", order, k4, k3, k1)
	}
	if c.bytes != int64(len(a)+len(c3)+len(d)) {
		t.Fatalf("locked bytes = %d, want %d", c.bytes, len(a)+len(c3)+len(d))
	}
}

func TestCachePutExpiryReclamationAccounting(t *testing.T) {
	c := NewCache(300, time.Hour)
	k1, k2, k3, k4, k5 := cacheKeyForETag("e1"), cacheKeyForETag("e2"), cacheKeyForETag("e3"), cacheKeyForETag("e4"), cacheKeyForETag("e5")
	a, b, c3, d, e5data := payload(60, 'a'), payload(90, 'b'), payload(70, 'c'), payload(80, 'd'), payload(100, 'e')
	c.Put(k1, a)
	c.Put(k2, b)
	c.Put(k3, c3)
	c.Put(k4, d)

	now := time.Now()
	c.mu.Lock()
	c.m[k2].Value.(*entry).expiresAt = now.Add(-time.Second)
	c.m[k4].Value.(*entry).expiresAt = now.Add(-time.Second)
	c.mu.Unlock()

	beforeBytes := c.Bytes()
	h0, m0, e0, x0 := c.Stats()
	if ev := c.Put(k5, e5data); ev != 0 {
		t.Fatalf("Put(k5) evicted %d entries, want 0 after reclaiming expired bytes", ev)
	}
	wantBytes := beforeBytes - int64(len(b)) - int64(len(d)) + int64(len(e5data))
	if c.Bytes() != wantBytes {
		t.Fatalf("Bytes() = %d, want %d", c.Bytes(), wantBytes)
	}
	if c.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", c.Len())
	}
	if h, m, e, x := c.Stats(); h != h0 || m != m0 || e != e0 || x != x0 {
		t.Fatalf("Stats after reclaim = %d/%d/%d/%d, want %d/%d/%d/%d", h, m, e, x, h0, m0, e0, x0)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k2]; ok {
		t.Fatal("expired k2 must be reclaimed")
	}
	if _, ok := c.m[k4]; ok {
		t.Fatal("expired k4 must be reclaimed")
	}
	for name, tc := range map[string]struct {
		key  CacheKey
		want []byte
	}{
		"k1": {key: k1, want: a},
		"k3": {key: k3, want: c3},
		"k5": {key: k5, want: e5data},
	} {
		el, ok := c.m[tc.key]
		if !ok {
			t.Fatalf("%s missing after reclaim-only overflow Put", name)
		}
		if got := el.Value.(*entry).data; !bytes.Equal(got, tc.want) {
			t.Fatalf("%s payload changed: got %q want %q", name, got, tc.want)
		}
	}
	if order := listOrder(c); !slices.Equal(order, []CacheKey{k5, k3, k1}) {
		t.Fatalf("LRU order = %v, want [%v %v %v]", order, k5, k3, k1)
	}
	var sum int64
	for el := c.ll.Front(); el != nil; el = el.Next() {
		sum += int64(len(el.Value.(*entry).data))
	}
	if sum != c.bytes {
		t.Fatalf("locked bytes = %d, want exact payload sum %d", c.bytes, sum)
	}
}

func TestCachePutReplaceOverflowReclaimsExpiredBeforeLiveEviction(t *testing.T) {
	c := NewCache(300, time.Hour)
	k1, k2, k3 := cacheKeyForETag("e1"), cacheKeyForETag("e2"), cacheKeyForETag("e3")
	a, b, c3, a2 := payload(100, 'a'), payload(80, 'b'), payload(120, 'c'), payload(160, 'A')
	c.Put(k1, a)
	c.Put(k2, b)
	c.Put(k3, c3)

	c.mu.Lock()
	c.m[k2].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
	c.mu.Unlock()

	h0, m0, e0, x0 := c.Stats()
	if ev := c.Put(k1, a2); ev != 0 {
		t.Fatalf("Put(k1 replace) evicted %d entries, want 0 after reclaiming expired bytes", ev)
	}
	if c.Len() != 2 || c.Bytes() != int64(len(a2)+len(c3)) {
		t.Fatalf("Len/Bytes = %d/%d, want 2/%d", c.Len(), c.Bytes(), len(a2)+len(c3))
	}
	if h, m, e, x := c.Stats(); h != h0 || m != m0 || e != e0 || x != x0 {
		t.Fatalf("Stats after replace reclaim = %d/%d/%d/%d, want %d/%d/%d/%d", h, m, e, x, h0, m0, e0, x0)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k2]; ok {
		t.Fatal("expired peer entry must be reclaimed before live eviction on replace")
	}
	for name, tc := range map[string]struct {
		key  CacheKey
		want []byte
	}{
		"k1": {key: k1, want: a2},
		"k3": {key: k3, want: c3},
	} {
		el, ok := c.m[tc.key]
		if !ok {
			t.Fatalf("%s missing after replace-only overflow Put", name)
		}
		if got := el.Value.(*entry).data; !bytes.Equal(got, tc.want) {
			t.Fatalf("%s payload changed: got %q want %q", name, got, tc.want)
		}
	}
	if order := listOrder(c); !slices.Equal(order, []CacheKey{k1, k3}) {
		t.Fatalf("LRU order = %v, want [%v %v]", order, k1, k3)
	}
}

func TestCachePutTTLDisabledPreservesLRU(t *testing.T) {
	c := NewCache(300, 0)
	k1, k2, k3, k4 := cacheKeyForETag("e1"), cacheKeyForETag("e2"), cacheKeyForETag("e3"), cacheKeyForETag("e4")
	a, b, c3, d := payload(100, 'a'), payload(100, 'b'), payload(100, 'c'), payload(100, 'd')
	c.Put(k1, a)
	c.Put(k2, b)
	c.Put(k3, c3)

	c.mu.Lock()
	c.m[k2].Value.(*entry).expiresAt = time.Now().Add(-time.Second)
	c.mu.Unlock()

	if ev := c.Put(k4, d); ev != 1 {
		t.Fatalf("Put(k4) evicted %d entries, want 1 on ttl=0 pure-LRU overflow", ev)
	}
	if c.Len() != 3 || c.Bytes() != 300 {
		t.Fatalf("Len/Bytes = %d/%d, want 3/300", c.Len(), c.Bytes())
	}
	if h, m, e, x := c.Stats(); h != 0 || m != 0 || e != 1 || x != 0 {
		t.Fatalf("Stats = %d/%d/%d/%d, want 0/0/1/0", h, m, e, x)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k1]; ok {
		t.Fatal("ttl=0 overflow must evict the live LRU tail k1")
	}
	for name, tc := range map[string]struct {
		key  CacheKey
		want []byte
	}{
		"k2": {key: k2, want: b},
		"k3": {key: k3, want: c3},
		"k4": {key: k4, want: d},
	} {
		el, ok := c.m[tc.key]
		if !ok {
			t.Fatalf("%s missing after ttl=0 overflow Put", name)
		}
		if got := el.Value.(*entry).data; !bytes.Equal(got, tc.want) {
			t.Fatalf("%s payload changed: got %q want %q", name, got, tc.want)
		}
	}
	if order := listOrder(c); !slices.Equal(order, []CacheKey{k4, k3, k2}) {
		t.Fatalf("LRU order = %v, want [%v %v %v]", order, k4, k3, k2)
	}
}
