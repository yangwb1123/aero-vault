// Package server assembles the production HTTP middleware chain. It exists so
// that cmd/server (package main) and the integration harness share one symbol
// (FR-1/AC-1 of docs/requirements/internal-integration-harness-12ring-chain-v1.md):
// the 12-ring chain cannot drift between production and tests because both call
// the same BuildChain/ApplyMiddleware, and the chain shape is data that a unit
// test can pin ring-by-ring.
package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// Ring names are the canonical identifiers of the 12 middleware rings. Each
// name doubles as the `middleware` label of telemetry.WithMiddlewareTiming and
// is pinned by TestBuildChain_12RingsInOrder: removing or reordering a ring
// fails the suite by construction.
const (
	RingAccessLog     = "access_log"
	RingConcurrency   = "concurrency"
	RingRecoverer     = "recoverer"
	RingOTel          = "otel"
	RingRateLimit     = "rate_limit"
	RingTenant        = "tenant"
	RingAuth          = "auth"
	RingMaxBody       = "max_body"
	RingSecureHeaders = "secure_headers"
	RingCORS          = "cors"
	RingBucketCORS    = "cors_bucket"
	RingRequestID     = "request_id"
)

// ChainLink is one middleware ring; Name is the ring identifier and the timing
// metric label.
type ChainLink struct {
	Name string
	MW   func(http.Handler) http.Handler
}

// BuildChain returns the 12-ring chain in registration order (outermost ring is
// the last element, request_id). The body is the verbatim migration of the
// former cmd/server/http.go applyMiddleware (zero production drift); the
// tenant-status lookup, the CORS ExposeHeaders append and the per-ring timing
// wrapper all live here, once.
//
// concurrencyMW must not be nil: dropping a ring silently would recreate the
// exact defect class this package exists to prevent, so a nil value is an
// assembly bug and fails fast at construction time (production main.go always
// passes NewConcurrencyLimiter(...).Middleware() or the per-tenant limiter;
// the harness passes NewConcurrencyLimiter(0).Middleware()).
func BuildChain(repo repository.Repository, authReg *auth.Registry, rl *middleware.RateLimiter, cfg *config.Config, logger *slog.Logger, concurrencyMW func(http.Handler) http.Handler, corsProvider middleware.BucketCORSProvider) []ChainLink {
	if concurrencyMW == nil {
		panic("server: concurrency middleware is nil — the 12-ring chain must not silently degrade to 11 rings")
	}
	tenantMW := middleware.TenantWithStatus(func(ctx context.Context, tenant string) (string, bool, error) {
		record, found, err := repo.GetTenant(ctx, tenant)
		return record.Status, found, err
	})
	return []ChainLink{
		{RingAccessLog, middleware.AccessLog(logger)},
		{RingConcurrency, concurrencyMW},
		{RingRecoverer, middleware.Recoverer(logger)},
		{RingOTel, telemetry.HTTPMiddleware("aero-vault")},
		{RingRateLimit, rl.Middleware()},
		{RingTenant, tenantMW},
		{RingAuth, authReg.Middleware()},
		{RingMaxBody, middleware.MaxBodySize(int64(cfg.App.MaxBodySize))},
		{RingSecureHeaders, middleware.SecureHeaders()},
		{RingCORS, middleware.CORS(middleware.CORSConfig{
			AllowedOrigins: cfg.CORS.AllowedOrigins,
			AllowedHeaders: cfg.CORS.AllowedHeaders,
			AllowedMethods: cfg.CORS.AllowedMethods,
			ExposeHeaders:  append([]string{"ETag", "Idempotency-Replayed", "Retry-After", "X-Request-ID", "X-Version-Id"}, cfg.CORS.ExposeHeaders...),
		})},
		{RingBucketCORS, middleware.BucketCORS(corsProvider)},
		{RingRequestID, middleware.RequestID},
	}
}

// ApplyMiddleware wraps handler with every ring, each instrumented with
// telemetry.WithMiddlewareTiming, applying rings from outermost (request_id,
// the last element of BuildChain) to innermost (access_log). This is the
// single assembly point for both production (cmd/server/main.go) and the
// integration harness.
func ApplyMiddleware(handler http.Handler, repo repository.Repository, authReg *auth.Registry, rl *middleware.RateLimiter, cfg *config.Config, logger *slog.Logger, concurrencyMW func(http.Handler) http.Handler, corsProvider middleware.BucketCORSProvider) http.Handler {
	for _, link := range BuildChain(repo, authReg, rl, cfg, logger, concurrencyMW, corsProvider) {
		handler = telemetry.WithMiddlewareTiming(link.Name, link.MW)(handler)
	}
	return handler
}
