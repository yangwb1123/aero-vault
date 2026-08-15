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
func countingOpener3(data []byte, etag string, opens *atomic.Int64) open3 {
	return func() (io.ReadCloser, string, error) {
		opens.Add(1)
		return io.NopCloser(bytes.NewReader(data)), etag, nil
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
		first, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", "e1", 32, 32, countingOpener3(data, "e1", &opens))
		if err != nil {
			t.Fatalf("call 1: %v", err)
		}
		if from {
			t.Fatal("non-MD5 source ETag must never report a cache hit")
		}
		second, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", "e1", 32, 32, countingOpener3(data, "e1", &opens))
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

		first, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", src, 32, 32, countingOpener3(data, src, &opens))
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
		second, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", src, 32, 32, countingOpener3(data, src, &opens))
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

// TestCacheGenerateSpyOpenerSingleInvocation pins REQ-2/A2: the second
// identical call is served from the cache — the opener is invoked exactly
// once — and the cached output is byte-identical to a fresh uncached decode
// of the same source (determinism pin).
func TestCacheGenerateSpyOpenerSingleInvocation(t *testing.T) {
	c := NewCache(1<<20, 0)
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	ctx := context.Background()

	first, fromCache, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(data, etagA, &opens))
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if fromCache {
		t.Fatal("first call must be a miss")
	}
	second, fromCache, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(data, etagA, &opens))
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
	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	ctx := context.Background()

	// Warm the cache (miss; acquires and releases one slot).
	if _, fromCache, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(data, etagA, &opens)); err != nil || fromCache {
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
		img, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(data, etagA, &opens))
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

	out1, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(v1, etagA, &opens1))
	if err != nil || from {
		t.Fatalf("etagA first call: err=%v fromCache=%v", err, from)
	}
	out2, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagB, 32, 32, countingOpener3(v2, etagB, &opens2))
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
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagB, 32, 32, countingOpener3(v2, etagB, &opens2)); err != nil || !from {
		t.Fatalf("etagB repeat: err=%v fromCache=%v", err, from)
	}
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(v1, etagA, &opens1)); err != nil || !from {
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
		got, from, err := GenerateContextWithOpenerCached(ctx, cache, "t1", etagA, 32, 32, countingOpener3(data, etagA, &opens))
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

	if _, _, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(data, etagA, &opens)); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	img, from, err := GenerateContextWithOpenerCached(canceled, c, "t1", etagA, 32, 32, countingOpener3(data, etagA, &opens))
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

// TestCacheGenerateErrorNeverCached pins REQ-2/T-7.3: a failing miss leaves
// the cache empty and a subsequent identical call re-invokes the opener
// (miss again) — errors never populate the cache.
func TestCacheGenerateErrorNeverCached(t *testing.T) {
	c := NewCache(1<<20, 0)
	ctx := context.Background()

	bomb := headerOnlyPNG(t, 8193, 8, 8, 6) // declared width > MaxSourceDim
	var opens atomic.Int64
	_, _, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(bomb, etagA, &opens))
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversized source: err = %v, want ErrImageTooLarge", err)
	}
	if c.Len() != 0 {
		t.Fatalf("failed generation must not be cached: Len = %d", c.Len())
	}
	if _, _, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(bomb, etagA, &opens)); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("retry: err = %v, want ErrImageTooLarge again (miss on retry)", err)
	}
	if n := opens.Load(); n != 2 {
		t.Fatalf("opener invoked %d times, want 2 (each failing call re-opens)", n)
	}

	// Unsupported (non-image) bytes: same never-cached discipline.
	garbage := []byte("not an image at all, just text bytes")
	var opens2 atomic.Int64
	_, _, err = GenerateContextWithOpenerCached(ctx, c, "t1", etagB, 32, 32, countingOpener3(garbage, etagB, &opens2))
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

	img, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(data, etagB, &opens))
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
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 32, 32, countingOpener3(data, etagB, &opens)); err != nil || from {
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

	outA, from, err := GenerateContextWithOpenerCached(ctx, c, "tenantA", etagA, 32, 32, countingOpener3(data, etagA, &opensA))
	if err != nil || from {
		t.Fatalf("tenantA first: err=%v fromCache=%v", err, from)
	}
	// tenantA repeat: hit (no re-open).
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, "tenantA", etagA, 32, 32, countingOpener3(data, etagA, &opensA)); err != nil || !from {
		t.Fatalf("tenantA repeat: err=%v fromCache=%v", err, from)
	}
	if n := opensA.Load(); n != 1 {
		t.Fatalf("tenantA opener invoked %d times, want 1", n)
	}
	// tenantB first call: must MISS despite identical bytes+dims (tenant is
	// part of the key) and produce byte-identical output (content-addressed).
	outB, from, err := GenerateContextWithOpenerCached(ctx, c, "tenantB", etagA, 32, 32, countingOpener3(data, etagA, &opensB))
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
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, "tenantB", etagA, 32, 32, countingOpener3(data, etagA, &opensB)); err != nil || !from {
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

	_, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 0, 0, countingOpener3(data, etagA, &opens))
	if err != nil || from {
		t.Fatalf("(0,0): err=%v fromCache=%v", err, from)
	}
	// ?w=256&h=256 clamps to the same effective pair → hit, no re-open.
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 256, 256, countingOpener3(data, etagA, &opens)); err != nil || !from {
		t.Fatalf("(256,256) after (0,0): err=%v fromCache=%v (must share the clamped entry)", err, from)
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opener invoked %d times, want 1", n)
	}
	// (2048, 2048) then (9999, 9999): both clamp to HardMax → shared entry.
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagB, 2048, 2048, countingOpener3(data, etagB, &opens)); err != nil || from {
		t.Fatalf("(2048,2048): err=%v fromCache=%v", err, from)
	}
	if _, from, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagB, 9999, 9999, countingOpener3(data, etagB, &opens)); err != nil || !from {
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
	if _, _, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 128, 128, countingOpener3(fixture, etagA, &opens)); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := GenerateContextWithOpenerCached(ctx, c, "t1", etagA, 128, 128, countingOpener3(fixture, etagA, &opens)); err != nil {
			b.Fatal(err)
		}
	}
}
