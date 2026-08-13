package telemetry

import "context"

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
