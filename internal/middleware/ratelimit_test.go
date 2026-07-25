package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// evictIdle reclaims buckets untouched past the idle TTL (so a flood of unique,
// client-controlled tenant values can't grow the map without bound) while keeping
// active ones.
func TestRateLimiter_EvictsIdleBuckets(t *testing.T) {
	rl := NewRateLimiter(10, 10)
	now := time.Now()
	rl.buckets["fresh"] = &bucket{tokens: 5, last: now}
	rl.buckets["stale"] = &bucket{tokens: 5, last: now.Add(-rlIdleTTL - time.Minute)}
	rl.evictIdle(now)
	if _, ok := rl.buckets["stale"]; ok {
		t.Fatal("idle bucket should be evicted")
	}
	if _, ok := rl.buckets["fresh"]; !ok {
		t.Fatal("fresh bucket must remain")
	}
}

func TestNewRateLimiter_DisabledReturnsNil(t *testing.T) {
	cases := []struct {
		name       string
		rps, burst float64
	}{
		{"both zero", 0, 0},
		{"zero rps", 0, 10},
		{"zero burst", 10, 0},
		{"negative rps", -1, 10},
		{"negative burst", 10, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rl := NewRateLimiter(c.rps, c.burst); rl != nil {
				t.Fatalf("NewRateLimiter(%v,%v) = %p, want nil", c.rps, c.burst, rl)
			}
		})
	}
}

func TestNewRateLimiter_Enabled(t *testing.T) {
	rl := NewRateLimiter(5, 10)
	if rl == nil {
		t.Fatal("NewRateLimiter(5,10) returned nil")
	}
	if rl.rps != 5 || rl.burst != 10 {
		t.Fatalf("rps=%v burst=%v", rl.rps, rl.burst)
	}
	if rl.buckets == nil {
		t.Fatal("buckets map not initialized")
	}
}

func TestAllow_NilPassThrough(t *testing.T) {
	var rl *RateLimiter // nil
	for i := 0; i < 100; i++ {
		ok, wait := rl.Allow("anyone")
		if !ok || wait != 0 {
			t.Fatalf("nil limiter must always allow; got ok=%v wait=%v", ok, wait)
		}
	}
}

// TestAllow_BurstThenDenied uses a vanishingly small refill rate so that the
// time elapsed during a tight loop adds effectively zero tokens. Exactly
// `burst` requests succeed, then the next is denied.
func TestAllow_BurstThenDenied(t *testing.T) {
	const burst = 5
	rl := NewRateLimiter(1e-9, burst) // refill ~0 over the test's lifetime

	for i := 0; i < burst; i++ {
		ok, wait := rl.Allow("t1")
		if !ok {
			t.Fatalf("request %d within burst should be allowed (wait=%v)", i+1, wait)
		}
	}
	ok, wait := rl.Allow("t1")
	if ok {
		t.Fatal("request beyond burst should be denied")
	}
	if wait <= 0 {
		t.Fatalf("denied request should report a positive Retry wait, got %v", wait)
	}
}

// TestAllow_PerTenantIsolation verifies each tenant gets its own bucket: one
// tenant exhausting its tokens does not affect another.
func TestAllow_PerTenantIsolation(t *testing.T) {
	const burst = 3
	rl := NewRateLimiter(1e-9, burst)

	// Drain tenant A.
	for i := 0; i < burst; i++ {
		if ok, _ := rl.Allow("A"); !ok {
			t.Fatalf("tenant A request %d should be allowed", i+1)
		}
	}
	if ok, _ := rl.Allow("A"); ok {
		t.Fatal("tenant A should now be exhausted")
	}

	// Tenant B is untouched and starts with a full bucket.
	for i := 0; i < burst; i++ {
		if ok, _ := rl.Allow("B"); !ok {
			t.Fatalf("tenant B request %d should be allowed (isolation broken)", i+1)
		}
	}
	if ok, _ := rl.Allow("B"); ok {
		t.Fatal("tenant B should now be exhausted")
	}
}

// TestAllow_RefillsOverTime exhausts a bucket, waits a few ms, and confirms
// tokens are replenished. With rps=1000, a >=5ms wait yields >=5 tokens, so at
// least one request must succeed again. The sleep guarantees a lower bound on
// elapsed time, keeping the assertion deterministic.
func TestAllow_RefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(1000, 2) // 1000 tokens/sec, burst 2

	// Drain the burst.
	for i := 0; i < 2; i++ {
		if ok, _ := rl.Allow("t"); !ok {
			t.Fatalf("initial burst request %d should be allowed", i+1)
		}
	}
	if ok, _ := rl.Allow("t"); ok {
		t.Fatal("bucket should be empty after burst")
	}

	time.Sleep(8 * time.Millisecond) // refills >= ~8 tokens, capped at burst=2

	if ok, _ := rl.Allow("t"); !ok {
		t.Fatal("bucket should have refilled after wait")
	}
}

// TestAllow_RefillCappedAtBurst makes sure idle time never accumulates tokens
// beyond burst: after a long-ish idle, only `burst` requests succeed.
func TestAllow_RefillCappedAtBurst(t *testing.T) {
	const burst = 3
	rl := NewRateLimiter(10000, burst)

	// One call to create the bucket and consume a token.
	if ok, _ := rl.Allow("t"); !ok {
		t.Fatal("first call should be allowed")
	}
	time.Sleep(10 * time.Millisecond) // would refill ~100 tokens uncapped

	allowed := 0
	for i := 0; i < burst+5; i++ {
		if ok, _ := rl.Allow("t"); ok {
			allowed++
		} else {
			break
		}
	}
	if allowed != burst {
		t.Fatalf("after idle, allowed=%d, want exactly burst=%d (tokens must cap)", allowed, burst)
	}
}

// --- Middleware() HTTP behavior ---

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestMiddleware_NilIsPassThrough(t *testing.T) {
	var rl *RateLimiter
	h := rl.Middleware()(okHandler())
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/objects", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("nil limiter middleware should pass through, got %d", rec.Code)
		}
	}
}

func TestMiddleware_Returns429WhenExhausted(t *testing.T) {
	rl := NewRateLimiter(1e-9, 1) // burst of 1
	h := rl.Middleware()(okHandler())

	// First request consumes the only token (tenant defaults to "default").
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/v1/objects", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v1/objects", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", rec2.Code)
	}
	if ra := rec2.Header().Get("Retry-After"); ra == "" {
		t.Fatal("429 response must set Retry-After header")
	}
}

func TestMiddleware_BypassPaths(t *testing.T) {
	rl := NewRateLimiter(1e-9, 1) // a single token shared by non-bypass paths
	h := rl.Middleware()(okHandler())

	bypass := []string{"/healthz", "/readyz", "/metrics", "/openapi.json", "/docs", "/ui", "/ui/index.html"}
	// Hammer bypass paths well beyond the token budget; all must succeed.
	for _, p := range bypass {
		for i := 0; i < 5; i++ {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("bypass path %q request %d = %d, want 200", p, i+1, rec.Code)
			}
		}
	}
}

// TestStart_BackgroundEviction verifies the background goroutine periodically
// evicts stale buckets without waiting for the map to hit rlMaxBuckets.
func TestStart_BackgroundEviction(t *testing.T) {
	rl := NewRateLimiter(10, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl.mu.Lock()
	rl.buckets["stale"] = &bucket{tokens: 5, last: time.Now().Add(-rlIdleTTL - time.Minute)}
	rl.buckets["fresh"] = &bucket{tokens: 5, last: time.Now()}
	rl.mu.Unlock()

	rl.startWithInterval(ctx, time.Millisecond)

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		rl.mu.Lock()
		_, stillStale := rl.buckets["stale"]
		_, stillFresh := rl.buckets["fresh"]
		rl.mu.Unlock()
		if !stillStale && stillFresh {
			return // evicted stale, kept fresh
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background sweep did not evict stale bucket within 100ms")
}

// TestAllow_MapNeverExceedsMax verifies the bucket map stays bounded at
// rlMaxBuckets even when it is full of *active* buckets (so eviction frees
// nothing). The next distinct tenant must be rejected rather than admitted past
// the cap. Regression test for the off-by-one that let the map reach
// rlMaxBuckets+1.
func TestAllow_MapNeverExceedsMax(t *testing.T) {
	rl := NewRateLimiter(1e-9, 5)

	// Fill the map to exactly rlMaxBuckets with fresh (active) buckets so
	// evictIdle reclaims nothing.
	now := time.Now()
	rl.mu.Lock()
	for i := 0; i < rlMaxBuckets; i++ {
		rl.buckets["t"+strconv.Itoa(i)] = &bucket{tokens: rl.burst, last: now}
	}
	rl.mu.Unlock()

	// A brand-new tenant must be refused (map is full of active buckets).
	ok, wait := rl.Allow("overflow")
	if ok {
		t.Fatal("new tenant should be rejected when the map is full of active buckets")
	}
	if wait <= 0 {
		t.Fatalf("rejected request should report a positive retry wait, got %v", wait)
	}

	rl.mu.Lock()
	n := len(rl.buckets)
	_, admitted := rl.buckets["overflow"]
	rl.mu.Unlock()
	if admitted {
		t.Fatal("overflow tenant must not be added to the map")
	}
	if n > rlMaxBuckets {
		t.Fatalf("bucket map grew to %d, must never exceed rlMaxBuckets=%d", n, rlMaxBuckets)
	}
}

// TestAllow_AdmitsAfterEvictionFreesSlot verifies that when the map is full but
// some buckets are idle, eviction reclaims room and the new tenant is admitted.
func TestAllow_AdmitsAfterEvictionFreesSlot(t *testing.T) {
	rl := NewRateLimiter(1e-9, 5)

	now := time.Now()
	stale := now.Add(-rlIdleTTL - time.Minute)
	rl.mu.Lock()
	// One short of the cap with active buckets, plus one idle bucket to reach
	// the cap. Eviction drops the idle one, freeing a slot.
	for i := 0; i < rlMaxBuckets-1; i++ {
		rl.buckets["t"+strconv.Itoa(i)] = &bucket{tokens: rl.burst, last: now}
	}
	rl.buckets["idle"] = &bucket{tokens: rl.burst, last: stale}
	rl.mu.Unlock()

	ok, _ := rl.Allow("newcomer")
	if !ok {
		t.Fatal("newcomer should be admitted after eviction frees an idle slot")
	}
	rl.mu.Lock()
	n := len(rl.buckets)
	_, idleGone := rl.buckets["idle"]
	rl.mu.Unlock()
	if idleGone {
		t.Fatal("idle bucket should have been evicted")
	}
	if n > rlMaxBuckets {
		t.Fatalf("bucket map grew to %d, must never exceed rlMaxBuckets=%d", n, rlMaxBuckets)
	}
}

func TestMiddleware_PerTenantBuckets(t *testing.T) {
	rl := NewRateLimiter(1e-9, 1)
	h := rl.Middleware()(okHandler())

	doReq := func(tenant string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/objects", nil)
		if tenant != "" {
			// Same-package test: stash the tenant on context directly using the
			// unexported key, mimicking what the Tenant middleware does.
			req = req.WithContext(context.WithValue(req.Context(), ctxTenantID, tenant))
		}
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Tenant "alpha" gets its one token then 429.
	if c := doReq("alpha"); c != http.StatusOK {
		t.Fatalf("alpha first = %d, want 200", c)
	}
	if c := doReq("alpha"); c != http.StatusTooManyRequests {
		t.Fatalf("alpha second = %d, want 429", c)
	}
	// Tenant "beta" is unaffected.
	if c := doReq("beta"); c != http.StatusOK {
		t.Fatalf("beta first = %d, want 200 (per-tenant isolation)", c)
	}
}
