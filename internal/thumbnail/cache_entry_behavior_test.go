package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestCacheGenerateCacheStoreRefusalIsNotAnEviction(t *testing.T) {
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
