package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Fixture ETags. etagA and etagB are both exactly 32 lowercase hex — the
// whole-object content-MD5 shape ContentMD5ETag requires — and distinct, so
// every hit-asserting TestCacheGenerate* exercises the admissible cache-key
// path (the shape gate bypasses any other source ETag). "e1" is the
// non-content-derived fixture for the bypass test.
const (
	etagA = "0123456789abcdef0123456789abcdef"
	etagB = "fedcba9876543210fedcba9876543210"
)

// countingOpener3 is the 3-value (ETag-reporting) spy opener: records every
// invocation and serves data on a fresh NopCloser each time, reporting the
// given source ETag.
func testIdentity(tenant string) SourceIdentity {
	return SourceIdentity{TenantID: tenant, Bucket: "bucket", Key: "image", VersionID: "version"}
}

func countingOpener3(data []byte, etag string, opens *atomic.Int64, ids ...SourceIdentity) Opener {
	identity := testIdentity("t1")
	if len(ids) > 0 {
		identity = ids[0]
	}
	return func() (io.ReadCloser, OpenedSource, error) {
		opens.Add(1)
		return io.NopCloser(bytes.NewReader(data)), OpenedSource{Identity: identity, ETag: etag, Bound: true}, nil
	}
}

// TestCacheGenerateETagShapeGate pins the module-boundary shape gate (DR-1):
// GenerateContextWithOpenerCached itself enforces the content-MD5 ETag
// precondition on sourceETag instead of importing it from the REST adapter.
// A non-content-derived source ETag must bypass the cache entirely (no
// lookup, no store, zero telemetry, byte-identical to the uncached path);
// an admissible 32-hex source ETag must keep the full hit/miss mechanics.
func TestCacheGenerateETagShapeGate(t *testing.T) {
	t.Run("non-MD5 source ETag bypasses cache", func(t *testing.T) {
		c := NewCache(1<<20, 0) // enabled cache: the gate must fire despite a live LRU
		data := makePNG(t, 64, 64)
		var opens atomic.Int64
		ctx := context.Background()

		// openedETag == sourceETag, so today's equality rule would store —
		// this is the vulnerability the gate closes.
		first, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), "e1", 32, 32, countingOpener3(data, "e1", &opens))
		if err != nil {
			t.Fatalf("call 1: %v", err)
		}
		if from {
			t.Fatal("non-MD5 source ETag must never report a cache hit")
		}
		second, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), "e1", 32, 32, countingOpener3(data, "e1", &opens))
		if err != nil {
			t.Fatalf("call 2: %v", err)
		}
		if from {
			t.Fatal("non-MD5 source ETag must never report a cache hit")
		}
		if n := opens.Load(); n != 2 {
			t.Fatalf("opener invoked %d times, want exactly once per call (2)", n)
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatalf("non-MD5 key must never be stored: Len=%d Bytes=%d", c.Len(), c.Bytes())
		}
		// Byte-identical to the uncached reference and to each other.
		var ref atomic.Int64
		uncached, err := GenerateContextWithOpener(ctx, 32, 32, countingOpener(data, &ref))
		if err != nil {
			t.Fatalf("uncached reference: %v", err)
		}
		if !bytes.Equal(first, uncached) {
			t.Fatal("bypass output differs from a fresh uncached decode")
		}
		if !bytes.Equal(first, second) {
			t.Fatal("bypass outputs differ between identical calls")
		}
		assertSlotsReleased(t)
	})

	t.Run("32-hex source ETag admissible", func(t *testing.T) {
		c := NewCache(1<<20, 0)
		data := makePNG(t, 64, 64)
		var opens atomic.Int64
		ctx := context.Background()
		src := etagA

		first, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), src, 32, 32, countingOpener3(data, src, &opens))
		if err != nil {
			t.Fatalf("call 1: %v", err)
		}
		if from {
			t.Fatal("first call must be a miss")
		}
		if n := opens.Load(); n != 1 {
			t.Fatalf("opener invoked %d times after call 1, want 1", n)
		}
		if c.Len() != 1 || c.Bytes() == 0 {
			t.Fatalf("admissible key must store: Len=%d Bytes=%d", c.Len(), c.Bytes())
		}
		second, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), src, 32, 32, countingOpener3(data, src, &opens))
		if err != nil {
			t.Fatalf("call 2: %v", err)
		}
		if !from {
			t.Fatal("second call must be a cache hit")
		}
		if n := opens.Load(); n != 1 {
			t.Fatalf("opener invoked %d times after call 2, want 1 (hit must not re-open)", n)
		}
		if !bytes.Equal(first, second) {
			t.Fatal("hit output differs from the original miss output")
		}
	})
}

func TestCacheGenerateOldRepresentationMissesThenSelfHeals(t *testing.T) {
	cache := NewCache(1<<20, 0)
	identity := testIdentity("t1")
	oldKey := currentThumbnailCacheKey(identity, etagA, 32, 32)
	oldKey.Representation = representationTokenForJPEGQuality(alternateJPEGQuality())
	cache.Put(oldKey, []byte("old-representation"))
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	open := countingOpener3(data, etagA, &opens, identity)

	first, from, err := GenerateContextWithOpenerCached(context.Background(), cache, identity, etagA, 32, 32, open)
	if err != nil || from || bytes.Equal(first, []byte("old-representation")) {
		t.Fatalf("rollover miss: err=%v fromCache=%v oldBytes=%v", err, from, bytes.Equal(first, []byte("old-representation")))
	}
	if h, m, _, _ := cache.Stats(); h != 0 || m != 1 {
		t.Fatalf("rollover miss stats=%d/%d, want 0/1", h, m)
	}
	second, from, err := GenerateContextWithOpenerCached(context.Background(), cache, identity, etagA, 32, 32, open)
	if err != nil || !from || !bytes.Equal(first, second) {
		t.Fatalf("rollover hit: err=%v fromCache=%v sameBytes=%v", err, from, bytes.Equal(first, second))
	}
	if h, m, _, _ := cache.Stats(); h != 1 || m != 1 {
		t.Fatalf("rollover hit stats=%d/%d, want 1/1", h, m)
	}
	if opens.Load() != 1 {
		t.Fatalf("rollover opener calls=%d, want 1", opens.Load())
	}
	if cache.Len() != 2 {
		t.Fatalf("rollover cache entries=%d, want 2 (old entry remains unreachable)", cache.Len())
	}
}

// TestCacheGenerateSpyOpenerSingleInvocation pins REQ-2/A2: the second
// identical call is served from the cache — the opener is invoked exactly
// once — and the cached output is byte-identical to a fresh uncached decode
// of the same source (determinism pin).
func TestCacheGenerateSpyOpenerSingleInvocation(t *testing.T) {
	c := NewCache(1<<20, 0)
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	ctx := context.Background()

	first, fromCache, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens))
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if fromCache {
		t.Fatal("first call must be a miss")
	}
	second, fromCache, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens))
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !fromCache {
		t.Fatal("second call must be a cache hit")
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opener invoked %d times, want exactly once", n)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("hit output differs from the original miss output")
	}
	// Determinism pin: the cached bytes equal a fresh uncached decode.
	uncached, err := GenerateContextWithOpener(ctx, 32, 32, countingOpener(data, &opens))
	if err != nil {
		t.Fatalf("uncached decode: %v", err)
	}
	if !bytes.Equal(first, uncached) {
		t.Fatal("cached bytes differ from a fresh uncached decode")
	}
}

// TestCacheGenerateHitBypassesDecodeSlot pins REQ-2/A2 + the run-2 gate P0
// arm: a hit under full 4-slot saturation returns immediately (2 s guard) —
// it never acquires decodeSlots, never re-invokes the opener — and the
// semaphore is fully recovered afterwards.
func TestCacheGenerateHitBypassesDecodeSlot(t *testing.T) {
	c := NewCache(1<<20, 0)
	admission := NewDecodeAdmission(1)
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	ctx := context.Background()

	// Warm the cache (miss; acquires and releases one slot).
	if _, fromCache, err := GenerateContextWithOpenerCachedWithAdmission(ctx, c, admission, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens)); err != nil || fromCache {
		t.Fatalf("warm-up: err=%v fromCache=%v", err, fromCache)
	}

	// Saturate all 4 slots; a hit must not touch the semaphore.
	slotSaturate()
	defer releaseAndRecoverSlots(t)

	done := make(chan struct {
		img  []byte
		from bool
		err  error
	}, 1)
	go func() {
		img, from, err := GenerateContextWithOpenerCachedWithAdmission(ctx, c, admission, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens))
		done <- struct {
			img  []byte
			from bool
			err  error
		}{img, from, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("hit under saturation: %v", res.err)
		}
		if !res.from {
			t.Fatal("call under saturation must be a hit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cache hit parked on the decode slot (must never touch the semaphore)")
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opener invoked %d times after warm-up, want 1", n)
	}
}

// TestCacheGenerateETagChangeMisses pins REQ-2/REQ-3/A3 (simulated PUT): a
// changed source ETag observes a miss and produces the fresh version's bytes;
// stale bytes are never served; repeats hit per key. Both fixtures are
// admissible 32-hex values — the changed-object semantics live inside the
// admissible class.
func TestCacheGenerateETagChangeMisses(t *testing.T) {
	c := NewCache(1<<20, 0)
	v1 := makePNG(t, 64, 64)
	v2 := makeJPEG(t, 64, 64)
	var opens1, opens2 atomic.Int64
	ctx := context.Background()

	out1, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(v1, etagA, &opens1))
	if err != nil || from {
		t.Fatalf("etagA first call: err=%v fromCache=%v", err, from)
	}
	out2, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagB, 32, 32, countingOpener3(v2, etagB, &opens2))
	if err != nil || from {
		t.Fatalf("etagB first call: err=%v fromCache=%v", err, from)
	}
	if n1, n2 := opens1.Load(), opens2.Load(); n1 != 1 || n2 != 1 {
		t.Fatalf("opener counts = %d/%d, want 1/1 (changed ETag observed a miss)", n1, n2)
	}
	if bytes.Equal(out1, out2) {
		t.Fatal("different source versions must produce different outputs")
	}
	// The etagB output is byte-identical to a fresh uncached decode of v2.
	var ref2 atomic.Int64
	uncached, err := GenerateContextWithOpener(ctx, 32, 32, countingOpener(v2, &ref2))
	if err != nil {
		t.Fatalf("uncached decode of v2: %v", err)
	}
	if !bytes.Equal(out2, uncached) {
		t.Fatal("stale bytes served: etagB output differs from a fresh decode of v2")
	}
	// Repeats hit per key; neither opener is re-invoked.
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagB, 32, 32, countingOpener3(v2, etagB, &opens2)); err != nil || !from {
		t.Fatalf("etagB repeat: err=%v fromCache=%v", err, from)
	}
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(v1, etagA, &opens1)); err != nil || !from {
		t.Fatalf("etagA repeat: err=%v fromCache=%v", err, from)
	}
	if n1, n2 := opens1.Load(), opens2.Load(); n1 != 1 || n2 != 1 {
		t.Fatalf("opener counts after repeats = %d/%d, want 1/1", n1, n2)
	}
}

// TestCacheGenerateDisabledParity pins REQ-2/REQ-6 (T-7.1): a nil cache and a
// zero-budget cache behave exactly like GenerateContextWithOpener — opener
// invoked exactly once per call, byte-identical output, full slot recovery.
func TestCacheGenerateDisabledParity(t *testing.T) {
	data := makePNG(t, 64, 64)
	ctx := context.Background()
	reference, err := GenerateContextWithOpener(ctx, 32, 32, countingOpener(data, &atomic.Int64{}))
	if err != nil {
		t.Fatalf("reference decode: %v", err)
	}
	for _, cache := range []*Cache{nil, NewCache(0, 0)} {
		var opens atomic.Int64
		got, from, err := GenerateContextWithOpenerCached(ctx, cache, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens))
		if err != nil {
			t.Fatalf("cached entry point (cache=%v): %v", cache, err)
		}
		if from {
			t.Fatalf("cache=%v: disabled path must report fromCache=false", cache)
		}
		if n := opens.Load(); n != 1 {
			t.Fatalf("cache=%v: opener invoked %d times, want exactly once per call", cache, n)
		}
		if !bytes.Equal(got, reference) {
			t.Fatalf("cache=%v: output differs from GenerateContextWithOpener", cache)
		}
		assertSlotsReleased(t)
	}
}

// TestCacheGenerateHitHonorsCanceledContext pins REQ-2/T-7.2: a dead request
// never receives cached bytes — the canceled context surfaces immediately
// (handler classification: Canceled → silent return) and the opener is not
// invoked.
func TestCacheGenerateHitHonorsCanceledContext(t *testing.T) {
	c := NewCache(1<<20, 0)
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	ctx := context.Background()

	if _, _, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens)); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	img, from, err := GenerateContextWithOpenerCached(canceled, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled hit: err = %v, want context.Canceled", err)
	}
	if from || img != nil {
		t.Fatalf("canceled hit must return no bytes (fromCache=%v len=%d)", from, len(img))
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opener invoked %d times, want 1 (canceled hit must not open)", n)
	}
}

type initialErrProbeContext struct {
	context.Context
	passed chan struct{}
	once   sync.Once
}

func (c *initialErrProbeContext) Err() error {
	c.once.Do(func() { close(c.passed) })
	return c.Context.Err()
}

// TestCacheGenerateCanceledWhileWaitingOnCacheLockDoesNotRefreshHitOrLRU
// pins the cancellation window after the entry-point check but before a
// contended cache hit can mutate hit counters or LRU recency.
func TestCacheGenerateCanceledWhileWaitingOnCacheLockDoesNotRefreshHitOrLRU(t *testing.T) {
	c := NewCache(1<<20, 0)
	data := makePNG(t, 64, 64)
	target := testIdentity("t1")
	target.Key = "target"
	other := target
	other.Key = "other"
	var opens atomic.Int64
	ctx := context.Background()
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, target, etagA, 32, 32, countingOpener3(data, etagA, &opens, target)); err != nil || from {
		t.Fatalf("target warm-up: err=%v fromCache=%v", err, from)
	}
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, other, etagB, 32, 32, countingOpener3(data, etagB, &opens, other)); err != nil || from {
		t.Fatalf("other warm-up: err=%v fromCache=%v", err, from)
	}
	targetKey := currentThumbnailCacheKey(target, etagA, 32, 32)
	c.mu.Lock()
	beforeOrder := listOrder(c)
	beforeHits := c.hits
	if len(beforeOrder) != 2 || !reflect.DeepEqual(beforeOrder[1], targetKey) {
		c.mu.Unlock()
		t.Fatalf("target must be the non-front LRU entry: order=%v target=%v", beforeOrder, targetKey)
	}
	canceled, cancel := context.WithCancel(ctx)
	probe := &initialErrProbeContext{Context: canceled, passed: make(chan struct{})}
	done := make(chan struct {
		img  []byte
		from bool
		err  error
	}, 1)
	go func() {
		img, from, err := GenerateContextWithOpenerCached(probe, c, target, etagA, 32, 32, countingOpener3(data, etagA, &opens, target))
		done <- struct {
			img  []byte
			from bool
			err  error
		}{img, from, err}
	}()
	select {
	case <-probe.passed:
	case <-time.After(2 * time.Second):
		c.mu.Unlock()
		t.Fatal("cached-hit call did not pass the entry-point cancellation check")
	}
	cancel()
	c.mu.Unlock()

	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("contended canceled hit: err=%v, want context.Canceled", result.err)
		}
		if result.img != nil || result.from {
			t.Fatalf("contended canceled hit returned cache bytes: len=%d fromCache=%v", len(result.img), result.from)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("contended canceled hit did not return")
	}
	c.mu.Lock()
	afterOrder := listOrder(c)
	afterHits := c.hits
	c.mu.Unlock()
	if afterHits != beforeHits {
		t.Fatalf("canceled hit changed hits: before=%d after=%d", beforeHits, afterHits)
	}
	if !reflect.DeepEqual(afterOrder, beforeOrder) {
		t.Fatalf("canceled hit changed LRU order: before=%v after=%v", beforeOrder, afterOrder)
	}
	if opens.Load() != 2 {
		t.Fatalf("canceled hit invoked opener: opens=%d, want 2 warm-up calls", opens.Load())
	}
}
