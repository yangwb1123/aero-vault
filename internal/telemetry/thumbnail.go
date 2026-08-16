package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Thumbnail-cache domain counters. The instruments themselves live here
// (the wrapper trio's Add calls) rather than in metrics.go's var block so
// the metric.go file stays under the 500-line gate; initDomain binds them
// to whichever MeterProvider is installed at runtime.
var (
	mThumbnailCacheHits            metric.Int64Counter
	mThumbnailCacheMisses          metric.Int64Counter
	mThumbnailCacheEvictions       metric.Int64Counter
	mThumbnailCacheSwept           metric.Int64Counter
	mThumbnailCacheSweepRuns       metric.Int64Counter
	mThumbnailGenerationSuccess    metric.Int64Counter
	mThumbnailGenerationRejections metric.Int64Counter
)

// Thumbnail-cache domain counters. The wrapper trio mirrors the
// IncIndexerSkip pattern (metrics.go): counters are created lazily inside
// initDomain so they bind to whichever MeterProvider is installed at runtime;
// the wrappers are the only surface the internal/thumbnail package imports
// (metrics.go's var block and initDomain gained the three instruments so the
// counter surface lives with the rest of the domain). The entry point
// (GenerateContextWithOpenerCached) forwards from the request context, so the
// nil/disabled path counts nothing.

// IncThumbnailCacheHit counts one server-side thumbnail cache hit (the
// response was served without a decode slot, opener, or decode).
func IncThumbnailCacheHit(ctx context.Context) {
	initDomain()
	mThumbnailCacheHits.Add(ctx, 1)
}

// IncThumbnailCacheMiss counts one server-side thumbnail cache miss (the
// request ran the full decode pipeline).
func IncThumbnailCacheMiss(ctx context.Context) {
	initDomain()
	mThumbnailCacheMisses.Add(ctx, 1)
}

// IncThumbnailCacheEviction counts n entries evicted from the server-side
// thumbnail cache by byte-budget pressure.
func IncThumbnailCacheEviction(ctx context.Context, n int64) {
	initDomain()
	mThumbnailCacheEvictions.Add(ctx, n)
}

// IncThumbnailCacheSwept counts n entries physically purged from the
// server-side thumbnail cache by the TTL sweep timer driver (not a read, not
// an LRU eviction — Cache.Stats is untouched).
func IncThumbnailCacheSwept(ctx context.Context, n int64) {
	initDomain()
	mThumbnailCacheSwept.Add(ctx, n)
}

// IncThumbnailCacheSweepRun counts one sweep pass EXECUTED by the driver —
// regardless of how many entries were removed (even 0). It is the control's
// liveness signal: swept_total alone reads zero both when the driver is
// healthy-but-idle and when it is dead, so the stalled-sweep alert keys off
// the per-pass counter instead.
func IncThumbnailCacheSweepRun(ctx context.Context) {
	initDomain()
	mThumbnailCacheSweepRuns.Add(ctx, 1)
}

// IncThumbnail304 counts one certified 304 revalidation of the derived
// thumbnail resource (the If-None-Match fast path: three repo point reads,
// no stream, no decode slot). Bounded client freshness (max-age=300,
// must-revalidate) converts silent cache hits into these revalidations, so
// the counter makes the revalidation traffic class observable.
func IncThumbnail304(ctx context.Context) {
	initDomain()
	mThumbnail304.Add(ctx, 1)
}

// IncThumbnailGenerationSuccess counts one thumbnail derivation request that
// resolves to HTTP 200 — cache hit or miss alike; the 200 outcome is the
// contract, not the decode path (a cache hit serves the stored JPEG with no
// slot, opener, or decode but is still a successful derivation response). 304s
// are excluded: IncThumbnail304 already covers the revalidation fast path.
func IncThumbnailGenerationSuccess(ctx context.Context) {
	initDomain()
	mThumbnailGenerationSuccess.Add(ctx, 1)
}

// IncThumbnailGenerationRejection counts one derivation-phase rejection tagged
// with exactly one reason label (the closed set produced by the REST boundary's
// thumbnailRejectionReason: image_too_large | metadata_too_large |
// source_too_large | unsupported_format | not_an_image | unsupported |
// invalid_argument | source_error | timeout). Silent client disconnects are not
// counted — no response is emitted for them.
func IncThumbnailGenerationRejection(ctx context.Context, reason string) {
	initDomain()
	mThumbnailGenerationRejections.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}
