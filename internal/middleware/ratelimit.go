package middleware

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter is a per-tenant token-bucket. The default bucket gates clients
// that haven't been assigned a tenant. When RPS or Burst is zero, the
// middleware is a pass-through.
type RateLimiter struct {
	rps     float64
	burst   float64
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	// rlMaxBuckets caps the per-tenant bucket map. The tenant key comes from the
	// client-controlled X-Aero-Tenant header, so without a bound a flood of unique
	// values would grow the map without limit (memory DoS).
	rlMaxBuckets = 50_000
	// rlIdleTTL is how long an untouched bucket lives before eviction. A bucket idle
	// this long has refilled to full, so dropping it loses no rate-limit state.
	rlIdleTTL = 10 * time.Minute
	// rlEvictInterval is how often the background sweep runs.
	rlEvictInterval = 60 * time.Second
)

func NewRateLimiter(rps, burst float64) *RateLimiter {
	if rps <= 0 || burst <= 0 {
		return nil
	}
	return &RateLimiter{rps: rps, burst: burst, buckets: make(map[string]*bucket)}
}

// Start launches a background goroutine that evicts idle buckets every
// rlEvictInterval. It returns immediately; the goroutine stops when ctx is
// cancelled. Safe to call on a nil receiver (no-op).
func (rl *RateLimiter) Start(ctx context.Context) {
	rl.startWithInterval(ctx, rlEvictInterval)
}

func (rl *RateLimiter) startWithInterval(ctx context.Context, interval time.Duration) {
	if rl == nil {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				rl.mu.Lock()
				rl.evictIdle(now)
				rl.mu.Unlock()
			}
		}
	}()
}

// evictIdle drops buckets untouched for longer than rlIdleTTL. Caller holds rl.mu.
func (rl *RateLimiter) evictIdle(now time.Time) {
	for k, b := range rl.buckets {
		if now.Sub(b.last) > rlIdleTTL {
			delete(rl.buckets, k)
		}
	}
}

func (rl *RateLimiter) Allow(tenant string) (bool, time.Duration) {
	if rl == nil {
		return true, 0
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[tenant]
	now := time.Now()
	if !ok {
		// Bound the map: when it's full, reclaim idle buckets before adding a new one.
		if len(rl.buckets) >= rlMaxBuckets {
			rl.evictIdle(now)
		}
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[tenant] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * rl.rps
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	deficit := 1 - b.tokens
	wait := time.Duration(deficit / rl.rps * float64(time.Second))
	return false, wait
}

// Middleware returns the http.Handler chain enforcer. System endpoints
// (/healthz, /readyz, /ui) bypass the limiter so dashboards/probes can't lock
// the bucket. Requests without a tenant share the "default" bucket.
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	if rl == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	bypass := func(p string) bool {
		return p == "/healthz" || p == "/readyz" || p == "/metrics" ||
			p == "/openapi.json" || p == "/docs" ||
			p == "/ui" || len(p) >= 4 && p[:4] == "/ui/"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bypass(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			t := TenantFrom(r.Context())
			if t == "" {
				t = "default"
			}
			ok, wait := rl.Allow(t)
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
