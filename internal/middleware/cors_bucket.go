package middleware

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// BucketCORSProvider is the interface for fetching per-bucket CORS rules.
// Implementations may cache results.
type BucketCORSProvider interface {
	// GetCORSRules returns the CORS rules for a given tenant+bucket.
	GetCORSRules(ctx context.Context, tenant, bucket string) ([]repository.CORSRule, error)
	// InvalidateBucket clears the cached CORS rules for a given tenant+bucket.
	InvalidateBucket(ctx context.Context, tenant, bucket string)
}

// bucketCORSProvider is a cached implementation of BucketCORSProvider.
type bucketCORSProvider struct {
	repo repository.Repository

	mu        sync.RWMutex
	entries   map[bucketKey]*cacheEntry
	ttl       time.Duration
	closeCh   chan struct{}
	closeOnce sync.Once
}

type bucketKey struct {
	tenant string
	bucket string
}

type cacheEntry struct {
	rules     []repository.CORSRule
	expiresAt time.Time
}

// NewBucketCORSProvider creates a provider that caches bucket CORS rules in
// memory with the given TTL. The cleanup goroutine must be stopped by calling
// Close when the provider is no longer needed.
func NewBucketCORSProvider(repo repository.Repository, ttl time.Duration) *bucketCORSProvider {
	p := &bucketCORSProvider{
		repo:    repo,
		entries: make(map[bucketKey]*cacheEntry),
		ttl:     ttl,
		closeCh: make(chan struct{}),
	}
	if ttl > 0 {
		go p.evictLoop()
	}
	return p
}

func (p *bucketCORSProvider) GetCORSRules(ctx context.Context, tenant, bucket string) ([]repository.CORSRule, error) {
	key := bucketKey{tenant: tenant, bucket: bucket}

	// Fast path: check cache.
	p.mu.RLock()
	entry, ok := p.entries[key]
	p.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.rules, nil
	}

	// Slow path: fetch from repository.
	rules, err := p.repo.GetBucketCORS(ctx, tenant, bucket)
	if err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []repository.CORSRule{}
	}

	p.mu.Lock()
	p.entries[key] = &cacheEntry{
		rules:     rules,
		expiresAt: time.Now().Add(p.ttl),
	}
	p.mu.Unlock()
	return rules, nil
}

func (p *bucketCORSProvider) InvalidateBucket(ctx context.Context, tenant, bucket string) {
	key := bucketKey{tenant: tenant, bucket: bucket}
	p.mu.Lock()
	delete(p.entries, key)
	p.mu.Unlock()
}

func (p *bucketCORSProvider) Close() {
	p.closeOnce.Do(func() {
		close(p.closeCh)
	})
}

func (p *bucketCORSProvider) evictLoop() {
	ticker := time.NewTicker(p.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.evictExpired()
		case <-p.closeCh:
			return
		}
	}
}

func (p *bucketCORSProvider) evictExpired() {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, entry := range p.entries {
		if now.After(entry.expiresAt) {
			delete(p.entries, key)
		}
	}
}

// CORSRulesFromContext reads the per-bucket CORS rules stashed in the request
// context by BucketCORS middleware. Returns nil when no rules were found.
func CORSRulesFromContext(ctx context.Context) []repository.CORSRule {
	rules, _ := ctx.Value(corsRulesKey).([]repository.CORSRule)
	return rules
}

type contextKey int

const corsRulesKey contextKey = iota

// BucketCORS returns middleware that enriches the request context with per-bucket
// CORS rules from a BucketCORSProvider. It must run after Tenant middleware so
// the tenant is available in the context. The CORS response headers are written
// on every response, not just OPTIONS preflights, matching S3 behaviour.
//
// When no bucket-specific rules are found (or the provider returns nil/empty),
// the global CORS config from CORS() is used as a fallback — this middleware
// only adds bucket-level overrides on top of the global defaults.
//
// The middleware must wrap CORS() middleware: the outer CORS() handles the
// global/allowed origins, and this inner middleware adds bucket-level headers.
func BucketCORS(provider BucketCORSProvider) func(http.Handler) http.Handler {
	if provider == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only apply when there's an Origin header (CORS request).
			if origin := r.Header.Get("Origin"); origin != "" {
				tenant := TenantFrom(r.Context())
				bucket := bucketFromPath(r)
				if bucket == "" {
					bucket = "default"
				}

				rules, err := provider.GetCORSRules(r.Context(), tenant, bucket)
				if err == nil && len(rules) > 0 {
					// Apply the first matching rule's headers.
					for _, rule := range rules {
						if originAllowed(origin, rule.AllowedOrigins) {
							w.Header().Set("Access-Control-Allow-Origin", origin)
							w.Header().Set("Vary", "Origin")
							if len(rule.AllowedMethods) > 0 {
								w.Header().Set("Access-Control-Allow-Methods", strings.Join(rule.AllowedMethods, ", "))
							}
							if len(rule.AllowedHeaders) > 0 {
								w.Header().Set("Access-Control-Allow-Headers", strings.Join(rule.AllowedHeaders, ", "))
							}
							if len(rule.ExposeHeaders) > 0 {
								w.Header().Set("Access-Control-Expose-Headers", strings.Join(rule.ExposeHeaders, ", "))
							}
							if rule.MaxAgeSeconds > 0 {
								w.Header().Set("Access-Control-Max-Age", strFromInt(rule.MaxAgeSeconds))
							}
							break
						}
					}
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// originAllowed checks whether the given origin matches any entry in the
// allowed list (which may contain "*").
func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(origin, a) {
			return true
		}
	}
	return false
}

// bucketFromPath attempts to extract a bucket name from the URL path.
// For /v1/files/<key> it returns "default".
// For /v1/buckets/<bucket>/... or /s3/<bucket>/... it returns the bucket name.
func bucketFromPath(r *http.Request) string {
	path := r.URL.Path
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 2 {
		switch parts[0] {
		case "v1":
			if len(parts) >= 3 && parts[1] == "buckets" {
				return parts[2]
			}
			// For /v1/files/..., use "default" bucket
			if parts[1] == "files" {
				return "default"
			}
		case "s3":
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}
