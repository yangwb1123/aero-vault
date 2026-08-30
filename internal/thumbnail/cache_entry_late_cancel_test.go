package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type lateCancelContext struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
	once sync.Once
}

func newLateCancelContext() *lateCancelContext {
	return &lateCancelContext{done: make(chan struct{})}
}

func (c *lateCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *lateCancelContext) Done() <-chan struct{}       { return c.done }

func (c *lateCancelContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *lateCancelContext) Value(any) any { return nil }

func (c *lateCancelContext) trip(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

func cacheEntryBytes(c *Cache, key CacheKey) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), el.Value.(*entry).data...), true
}

func lateCancelCacheKey(identity SourceIdentity, sourceETag string, maxW, maxH int) CacheKey {
	effW, effH := EffectiveDims(maxW, maxH)
	return CacheKey{
		Identity: identity, SourceETag: sourceETag,
		EffW: effW, EffH: effH, Version: CacheKeyVersion,
		Representation: currentRepresentationToken,
	}
}

func seedLateCancelCache(t *testing.T) (*Cache, CacheKey, []byte, int, int64) {
	t.Helper()
	cache := NewCache(1<<20, 0)
	identity := testIdentity("tenant-a")
	identity.Key = "seeded"
	key := lateCancelCacheKey(identity, etagB, 32, 32)
	payload := []byte("seeded-payload")
	if evicted := cache.Put(key, payload); evicted != 0 {
		t.Fatalf("seed cache evicted %d entries, want 0", evicted)
	}
	return cache, key, payload, cache.Len(), cache.Bytes()
}

func TestCachedGenerationLateCancelBeforeStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{name: "canceled", want: context.Canceled},
		{name: "deadline", want: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, seededKey, seededPayload, beforeLen, beforeBytes := seedLateCancelCache(t)
			identity := testIdentity("tenant-a")
			targetKey := lateCancelCacheKey(identity, etagA, 32, 32)
			ctx := newLateCancelContext()
			source := &closeFailingSource{
				Reader: bytes.NewReader(makePNG(t, 64, 64)),
				onClose: func() {
					ctx.trip(tc.want)
				},
			}

			img, fromCache, err := GenerateContextWithOpenerCached(
				ctx, cache, identity, etagA, 32, 32,
				func() (io.ReadCloser, OpenedSource, error) {
					return source, OpenedSource{Identity: identity, ETag: etagA, Bound: true}, nil
				},
			)
			if err != tc.want {
				t.Fatalf("err = %v, want exact %v", err, tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(err, %v) = false, want true", tc.want)
			}
			if fromCache {
				t.Fatal("fromCache = true, want false")
			}
			if len(img) != 0 {
				t.Fatalf("len(img) = %d, want 0", len(img))
			}
			if source.closed.Load() != 1 {
				t.Fatalf("source closed %d times, want 1", source.closed.Load())
			}
			if cache.Len() != beforeLen || cache.Bytes() != beforeBytes {
				t.Fatalf("cache changed: len=%d/%d bytes=%d/%d", cache.Len(), beforeLen, cache.Bytes(), beforeBytes)
			}
			seeded, ok := cacheEntryBytes(cache, seededKey)
			if !ok || !bytes.Equal(seeded, seededPayload) {
				t.Fatalf("seeded entry changed: present=%v payload=%q", ok, seeded)
			}
			if _, ok := cacheEntryBytes(cache, targetKey); ok {
				t.Fatal("late-canceled miss stored the target cache entry")
			}
			assertSlotsReleased(t)
		})
	}
}

func TestUncachedGenerationLateCancelBeforeReturn(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{name: "canceled", want: context.Canceled},
		{name: "deadline", want: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, seededKey, seededPayload, beforeLen, beforeBytes := seedLateCancelCache(t)
			identity := testIdentity("tenant-a")
			identity.VersionID = ""
			ctx := newLateCancelContext()
			source := &closeFailingSource{
				Reader: bytes.NewReader(makePNG(t, 64, 64)),
				onClose: func() {
					ctx.trip(tc.want)
				},
			}

			img, fromCache, err := GenerateContextWithOpenerCached(
				ctx, cache, identity, etagA, 32, 32,
				func() (io.ReadCloser, OpenedSource, error) {
					return source, OpenedSource{Identity: testIdentity("tenant-a"), ETag: etagA, Bound: true}, nil
				},
			)
			if err != tc.want {
				t.Fatalf("err = %v, want exact %v", err, tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(err, %v) = false, want true", tc.want)
			}
			if fromCache {
				t.Fatal("fromCache = true, want false")
			}
			if len(img) != 0 {
				t.Fatalf("len(img) = %d, want 0", len(img))
			}
			if source.closed.Load() != 1 {
				t.Fatalf("source closed %d times, want 1", source.closed.Load())
			}
			if cache.Len() != beforeLen || cache.Bytes() != beforeBytes {
				t.Fatalf("cache changed: len=%d/%d bytes=%d/%d", cache.Len(), beforeLen, cache.Bytes(), beforeBytes)
			}
			seeded, ok := cacheEntryBytes(cache, seededKey)
			if !ok || !bytes.Equal(seeded, seededPayload) {
				t.Fatalf("seeded entry changed: present=%v payload=%q", ok, seeded)
			}
			assertSlotsReleased(t)
		})
	}
}
