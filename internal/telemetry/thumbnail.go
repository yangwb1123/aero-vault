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
	mThumbnailCacheExpired         metric.Int64Counter
	mThumbnailCacheSwept           metric.Int64Counter
	mThumbnailCacheSweepRuns       metric.Int64Counter
	mThumbnailCacheSweepLastRun    metric.Int64Gauge
	mThumbnailCacheSweepInterv     metric.Int64Gauge
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

// IncThumbnailCacheExpired counts one server-side thumbnail cache read that
// found its entry TTL-expired (entry removed, not served). Expired reads are
// a distinct class from misses: they contribute to neither the hit-ratio
// miss class nor the eviction counters (see cache.go GetOutcome), so the
// hit-ratio panel and ThumbnailCacheHitRatioLow measure genuine
// effectiveness — a sparse-TTL workload (TTL below the hot-key inter-request
// gap) raises this counter instead of depressing the hit ratio. This is
// monitoring telemetry (CC7.3 observability), not audit evidence under
// ISO 27001 A.12.4.1 — per-event audit records for the retention control
// belong to the platform L0/L1/L2 audit hierarchy and are out of scope here.
func IncThumbnailCacheExpired(ctx context.Context) {
	initDomain()
	mThumbnailCacheExpired.Add(ctx, 1)
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

// SetThumbnailCacheSweepCadence records the sweep driver's cadence envelope:
// the per-pass interval (seconds) and the wall-clock time of the most recent
// pass (unix seconds). The stalled-sweep alert is derived from these gauges
// ((now - last_run) > 3 * interval) instead of a static lookback window, so
// a TTL-driven cadence anywhere in the validated 1s..1y envelope stays
// alert-correct — a static 1h window would permanently fire for TTL >= ~75m.
// The driver calls this on every pass (lastRun) and once at start (interval);
// zero values are never consulted by the alert (interval > 0 guard).
func SetThumbnailCacheSweepCadence(ctx context.Context, intervalSeconds, lastRunUnix int64) {
	initDomain()
	if mThumbnailCacheSweepInterv != nil {
		mThumbnailCacheSweepInterv.Record(ctx, intervalSeconds)
	}
	if mThumbnailCacheSweepLastRun != nil {
		mThumbnailCacheSweepLastRun.Record(ctx, lastRunUnix)
	}
}

// initThumbnailInstruments binds the thumbnail-domain instruments to the
// meter created by initDomain. It lives here — not in metrics.go — so the
// metric.go file stays under the 500-line gate (AGENTS.md); initDomain
// calls it once, inside the domainOnce closure, after creating the meter.
func initThumbnailInstruments(m metric.Meter) {
	mThumbnailCacheHits, _ = m.Int64Counter("thumbnail.cache.hits_total")
	mThumbnailCacheMisses, _ = m.Int64Counter("thumbnail.cache.misses_total")
	mThumbnailCacheEvictions, _ = m.Int64Counter("thumbnail.cache.evictions_total")
	mThumbnailCacheExpired, _ = m.Int64Counter("thumbnail.cache.expired_total")
	mThumbnailCacheSwept, _ = m.Int64Counter("thumbnail.cache.swept_total")
	mThumbnailCacheSweepRuns, _ = m.Int64Counter("thumbnail.cache.sweep_runs_total")
	mThumbnailCacheSweepLastRun, _ = m.Int64Gauge("thumbnail.cache.sweep_last_run_seconds")
	mThumbnailCacheSweepInterv, _ = m.Int64Gauge("thumbnail.cache.sweep_interval_seconds")
	mThumbnail304, _ = m.Int64Counter("thumbnail.304_total")
	mThumbnailGenerationSuccess, _ = m.Int64Counter("thumbnail.generation.success_total")
	mThumbnailGenerationRejections, _ = m.Int64Counter("thumbnail.generation.rejections_total")
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
