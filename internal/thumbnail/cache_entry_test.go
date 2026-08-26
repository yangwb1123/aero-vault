package thumbnail

// White-box tests for GenerateContextWithOpenerCached (cache_entry.go): the
// module-boundary content-MD5 shape gate, the spy-opener single-invocation
// pin, decode-slot bypass on hit, ETag-driven misses, disabled parity with
// GenerateContextWithOpener, ctx-on-hit classification, error-never-cached,
// ETag-verify-before-Put, cross-tenant isolation, clamped-dims key sharing,
// and the hit-path benchmark. All fixtures are deterministic; the only waits
// are the existing slot-park guards. -short and -race friendly within the
// 120 s test-race-thumbnail budget.

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

// TestCacheGenerateErrorNeverCached pins REQ-2/T-7.3: a failing miss leaves
// the cache empty and a subsequent identical call re-invokes the opener
// (miss again) — errors never populate the cache.
func TestCacheGenerateErrorNeverCached(t *testing.T) {
	c := NewCache(1<<20, 0)
	ctx := context.Background()

	bomb := headerOnlyPNG(t, 8193, 8, 8, 6) // declared width > MaxSourceDim
	var opens atomic.Int64
	_, _, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(bomb, etagA, &opens))
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversized source: err = %v, want ErrImageTooLarge", err)
	}
	if c.Len() != 0 {
		t.Fatalf("failed generation must not be cached: Len = %d", c.Len())
	}
	if _, _, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(bomb, etagA, &opens)); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("retry: err = %v, want ErrImageTooLarge again (miss on retry)", err)
	}
	if n := opens.Load(); n != 2 {
		t.Fatalf("opener invoked %d times, want 2 (each failing call re-opens)", n)
	}

	// Unsupported (non-image) bytes: same never-cached discipline.
	garbage := []byte("not an image at all, just text bytes")
	var opens2 atomic.Int64
	_, _, err = GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagB, 32, 32, countingOpener3(garbage, etagB, &opens2))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("garbage source: err = %v, want ErrUnsupported", err)
	}
	if c.Len() != 0 {
		t.Fatalf("unsupported generation must not be cached: Len = %d", c.Len())
	}
}

// TestCacheGenerateETagMismatchDoesNotStore pins the run-2 gate #1 binding
// condition: when the opened object's ETag differs from the key's source ETag
// (a PUT landed between the caller's Stat and the open), the success bytes
// are returned but never stored — a subsequent identical call re-opens. Both
// fixtures are admissible 32-hex values: the equality race rule fires within
// the admissible class (DR-3).
func TestCacheGenerateETagMismatchDoesNotStore(t *testing.T) {
	c := NewCache(1<<20, 0)
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	ctx := context.Background()

	img, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagB, &opens))
	if err != nil {
		t.Fatalf("mismatched open: %v", err)
	}
	if from {
		t.Fatal("first call must be a miss")
	}
	if len(img) == 0 {
		t.Fatal("success bytes must still be returned on mismatch")
	}
	if c.Len() != 0 {
		t.Fatalf("ETag-mismatched generation must not be stored: Len = %d", c.Len())
	}
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagB, &opens)); err != nil || from {
		t.Fatalf("repeat after mismatch: err=%v fromCache=%v (must miss and re-open)", err, from)
	}
	if n := opens.Load(); n != 2 {
		t.Fatalf("opener invoked %d times, want 2 (mismatch never stores)", n)
	}
}

// TestCacheGenerateCrossTenantIsolation pins REQ-3: identical bytes + key in
// two tenants each open exactly once — one tenant's cached bytes are never
// served to another tenant's requests.
func TestCacheGenerateCrossTenantIsolation(t *testing.T) {
	c := NewCache(1<<20, 0)
	data := makePNG(t, 64, 64)
	var opensA, opensB atomic.Int64
	ctx := context.Background()

	outA, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("tenantA"), etagA, 32, 32, countingOpener3(data, etagA, &opensA, testIdentity("tenantA")))
	if err != nil || from {
		t.Fatalf("tenantA first: err=%v fromCache=%v", err, from)
	}
	// tenantA repeat: hit (no re-open).
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("tenantA"), etagA, 32, 32, countingOpener3(data, etagA, &opensA, testIdentity("tenantA"))); err != nil || !from {
		t.Fatalf("tenantA repeat: err=%v fromCache=%v", err, from)
	}
	if n := opensA.Load(); n != 1 {
		t.Fatalf("tenantA opener invoked %d times, want 1", n)
	}
	// tenantB first call: must MISS despite identical bytes+dims (tenant is
	// part of the key) and produce byte-identical output (content-addressed).
	outB, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("tenantB"), etagA, 32, 32, countingOpener3(data, etagA, &opensB, testIdentity("tenantB")))
	if err != nil || from {
		t.Fatalf("tenantB first: err=%v fromCache=%v (must miss: cross-tenant isolation)", err, from)
	}
	if n := opensB.Load(); n != 1 {
		t.Fatalf("tenantB opener invoked %d times, want 1", n)
	}
	if !bytes.Equal(outA, outB) {
		t.Fatal("identical content must produce identical output across tenants")
	}
	// A's entry must never serve B: B's second call is a hit on B's own key.
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("tenantB"), etagA, 32, 32, countingOpener3(data, etagA, &opensB, testIdentity("tenantB"))); err != nil || !from {
		t.Fatalf("tenantB repeat: err=%v fromCache=%v", err, from)
	}
}

// TestCacheGenerateClampedDimsShareEntry pins the run-2 gate #2 key
// normalization: requests whose dims differ only in clamped-away values
// (?w=0 vs ?w=256; 2048 vs 9999) share one key — one open, one hit.
func TestCacheGenerateClampedDimsShareEntry(t *testing.T) {
	c := NewCache(1<<20, 0)
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	ctx := context.Background()

	_, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 0, 0, countingOpener3(data, etagA, &opens))
	if err != nil || from {
		t.Fatalf("(0,0): err=%v fromCache=%v", err, from)
	}
	// ?w=256&h=256 clamps to the same effective pair → hit, no re-open.
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 256, 256, countingOpener3(data, etagA, &opens)); err != nil || !from {
		t.Fatalf("(256,256) after (0,0): err=%v fromCache=%v (must share the clamped entry)", err, from)
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opener invoked %d times, want 1", n)
	}
	// (2048, 2048) then (9999, 9999): both clamp to HardMax → shared entry.
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagB, 2048, 2048, countingOpener3(data, etagB, &opens)); err != nil || from {
		t.Fatalf("(2048,2048): err=%v fromCache=%v", err, from)
	}
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagB, 9999, 9999, countingOpener3(data, etagB, &opens)); err != nil || !from {
		t.Fatalf("(9999,9999) after (2048,2048): err=%v fromCache=%v (must share the HardMax entry)", err, from)
	}
	if n := opens.Load(); n != 2 {
		t.Fatalf("opener invoked %d times, want 2", n)
	}
}

// BenchmarkGenerateContextWithOpenerCachedHit documents the hit-path cost:
// a seeded cache serves repeats with no decode, no slot, no opener. The
// first call (miss) is outside the loop. Benchmarks are documentation-quality
// (repo bench discipline — never asserted in CI).
func BenchmarkGenerateContextWithOpenerCachedHit(b *testing.B) {
	c := NewCache(1<<20, 0)
	fixture := benchFixture(b, 256, 256)
	var opens atomic.Int64
	ctx := context.Background()
	if _, _, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 128, 128, countingOpener3(fixture, etagA, &opens)); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 128, 128, countingOpener3(fixture, etagA, &opens)); err != nil {
			b.Fatal(err)
		}
	}
}

// TestCacheETagShapeGateDeadCtxAndStats pins QA F-1/F-2/F-3:
//   - F-1: a dead context fast-fails BEFORE the shape gate — a canceled
//     request with a non-MD5 ETag returns ctx.Err() (classification:
//     Canceled → silent, DeadlineExceeded → 504), never an opener call.
//   - F-2: bypass counts nothing — Stats() stays zero across bypass calls.
//   - F-3: every non-admissible shape class bypasses (the design's own
//     failure-mode table): empty, quoted, uppercase hex, multipart
//     "<md5>-<n>", 31/33-char, dash-in-32, non-hex-in-32, mixed case.
func TestCacheETagShapeGateDeadCtxAndStats(t *testing.T) {
	data := makePNG(t, 64, 64)

	// F-1: dead ctx precedes the shape gate.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewCache(1<<20, 0)
	var opens atomic.Int64
	_, _, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), "e1", 32, 32, countingOpener3(data, "e1", &opens))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dead ctx + non-MD5: err = %v, want context.Canceled (fast-fail precedes the shape gate)", err)
	}
	if n := opens.Load(); n != 0 {
		t.Fatalf("opener invoked %d times on a dead request, want 0", n)
	}

	// F-2 + F-3: the multi-shape-class matrix bypasses with zero counting.
	badShapes := []string{
		"",                                   // empty
		`"0123456789abcdef0123456789abcdef"`, // quoted
		"0123456789ABCDEF0123456789ABCDEF",   // uppercase hex
		"abc123def4567890abcdef1234567890-3", // multipart
		"0123456789abcdef0123456789abcde",    // 31 chars
		"0123456789abcdef0123456789abcdef0",  // 33 chars
		"0123456789abcdef-1234567890abcdef",  // dash inside 32
		"0123456789abcdef0123456789abcdeg",   // non-hex inside 32
		"0123456789ABCDEF0123456789abcdef",   // mixed case
	}
	for _, et := range badShapes {
		t.Run("shape "+et[:min(len(et), 12)], func(t *testing.T) {
			c := NewCache(1<<20, 0)
			var opens atomic.Int64
			out, from, err := GenerateContextWithOpenerCached(context.Background(), c, testIdentity("t1"), et, 32, 32, countingOpener3(data, et, &opens))
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if from {
				t.Fatal("non-MD5 shape must never report a hit")
			}
			if opens.Load() != 1 {
				t.Fatalf("opener invoked %d times, want 1", opens.Load())
			}
			if c.Len() != 0 || c.Bytes() != 0 {
				t.Fatalf("non-MD5 shape must never be stored: Len=%d Bytes=%d", c.Len(), c.Bytes())
			}
			// F-2: zero counting — Stats is the observable proxy.
			hits, misses, evictions, expired := c.Stats()
			if hits != 0 || misses != 0 || evictions != 0 || expired != 0 {
				t.Fatalf("bypass counted: hits=%d misses=%d evictions=%d expired=%d, want all zero", hits, misses, evictions, expired)
			}
			if len(out) == 0 {
				t.Fatal("bypass must still produce the thumbnail bytes")
			}
		})
	}
}

func TestGenerateCacheEvictionsForwarded(t *testing.T) {
	fixture := makePNG(t, 128, 128)
	probe := NewCache(1<<20, 0)
	first, _, err := GenerateContextWithOpenerCached(context.Background(), probe, testIdentity("t1"), etagA, 64, 64,
		countingOpener3(fixture, etagA, new(atomic.Int64)))
	if err != nil {
		t.Fatalf("probe generation: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("probe generation returned empty bytes")
	}
	c := NewCache(int64(len(first)), 0)
	if _, _, err := GenerateContextWithOpenerCached(context.Background(), c, testIdentity("t1"), etagA, 64, 64,
		countingOpener3(fixture, etagA, new(atomic.Int64))); err != nil {
		t.Fatalf("first cached generation: %v", err)
	}
	if _, _, err := GenerateContextWithOpenerCached(context.Background(), c, testIdentity("t1"), etagB, 64, 64,
		countingOpener3(fixture, etagB, new(atomic.Int64))); err != nil {
		t.Fatalf("overflow cached generation: %v", err)
	}
	_, _, evictions, _ := c.Stats()
	if evictions < 1 || c.Len() != 1 {
		t.Fatalf("cache stats after overflow: evictions=%d len=%d, want at least one eviction and one entry", evictions, c.Len())
	}
}

func TestGenerateCacheStoreRefusalIsNotAnEviction(t *testing.T) {
	c := NewCache(1, 0)
	img, from, err := GenerateContextWithOpenerCached(context.Background(), c, testIdentity("t1"), etagA, 64, 64,
		countingOpener3(makePNG(t, 128, 128), etagA, new(atomic.Int64)))
	if err != nil {
		t.Fatalf("oversized cache generation: %v", err)
	}
	if from || len(img) == 0 {
		t.Fatalf("oversized cache generation: from=%v len=%d", from, len(img))
	}
	_, _, evictions, _ := c.Stats()
	if c.Len() != 0 || evictions != 0 {
		t.Fatalf("store refusal changed cache: len=%d evictions=%d", c.Len(), evictions)
	}
}

// TestCacheGenerateExpiredReadRerunsMissBody pins the entry-point expired
// contract (AC-2 routing + upstream contract): a TTL-expired read through
// GenerateContextWithOpenerCached is classified as the expired class — the
// opener runs again (the miss body executes, so the fresh generation is
// produced and re-stored with a fresh expiry) and the cache's expired
// counter increments while misses stays at its genuine count. Before the
// fix this read counted as an ordinary miss, inflating the hit-ratio
// denominator; now it feeds neither hits nor misses.
func TestCacheGenerateExpiredReadRerunsMissBody(t *testing.T) {
	c := NewCache(1<<20, time.Hour)
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	ctx := context.Background()

	first, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens))
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if from {
		t.Fatal("first call on an empty cache must be a miss")
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opener invoked %d times after call 1, want 1", n)
	}
	if c.Len() != 1 {
		t.Fatalf("call 1 must store: Len=%d, want 1", c.Len())
	}

	// Backdate the single stored entry: the TTL has elapsed without wall
	// clock passing (deterministic, sleep-free).
	c.mu.Lock()
	for _, el := range c.m {
		el.Value.(*entry).expiresAt = time.Now().Add(-time.Second)
	}
	c.mu.Unlock()

	second, from, err := GenerateContextWithOpenerCached(ctx, c, testIdentity("t1"), etagA, 32, 32, countingOpener3(data, etagA, &opens))
	if err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if from {
		t.Fatal("an expired read must not report a cache hit")
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the regenerated bytes must equal the original generation")
	}
	if n := opens.Load(); n != 2 {
		t.Fatalf("opener invoked %d times total, want 2 (the expired read must re-run the miss body)", n)
	}
	// Re-stored with a fresh expiry: the entry is resident again.
	if c.Len() != 1 || c.Bytes() != int64(len(second)) {
		t.Fatalf("expired read must re-store the fresh generation: Len=%d Bytes=%d, want 1/%d", c.Len(), c.Bytes(), len(second))
	}
	// Accounting: the expired read is its own class — misses counts the
	// first call's genuine miss only; expired counts the second read.
	h, m, e, x := c.Stats()
	if h != 0 || m != 1 || e != 0 || x != 1 {
		t.Fatalf("Stats = %d/%d/%d/%d, want 0/1/0/1 (expired read must not feed the hit-ratio miss count)", h, m, e, x)
	}
}
