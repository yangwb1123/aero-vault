package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/telemetry"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// startThumbnailCacheSweep starts the TTL physical-purge timer driver for the
// server-side thumbnail cache — the pinned follow-up to Cache.SweepExpired.
// Cadence: RECONCILE_INTERVAL_MINUTES (the same interval driving the Reconcile
// ticker, AGENTS.md §2.4). When the interval is 0 (reconcile disabled, the
// default) no driver starts and the documented lazy-only bound applies. The
// driver is a goroutine owned by cmd/server — never by Cache (which spawns no
// goroutines and owns no timers) and never on the request path.
func startThumbnailCacheSweep(ctx context.Context, cache *thumbnail.Cache, interval time.Duration, logger *slog.Logger) {
	if cache == nil || interval <= 0 {
		return
	}
	go runThumbnailCacheSweep(ctx, cache, interval, logger)
	logger.Info("thumbnail cache sweep started", "interval", interval.String())
}

// runThumbnailCacheSweep runs an initial sweep immediately, then one sweep per
// interval, until ctx is canceled — the exact ticker shape of reconcile.Job.Run
// (internal/reconcile/job.go). Per-process, never wrapped in
// RECONCILE_CLUSTER_SINGLETON leasing (REQ-1.5): the thumbnail cache is
// in-process memory; only the local process can purge its own memory, and
// gating the sweep on the singleton lease would leak physical retention on
// every non-holder replica. SweepExpired is an O(1) no-op (no lock) when the
// cache is disabled or TTL <= 0, so an unarmed driver costs nothing per tick.
func runThumbnailCacheSweep(ctx context.Context, cache *thumbnail.Cache, interval time.Duration, logger *slog.Logger) {
	if cache == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sweepThumbnailCache(ctx, cache, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepThumbnailCache(ctx, cache, logger)
		}
	}
}

// sweepThumbnailCache performs one physical purge pass and forwards the removed
// count to telemetry. Cache.Stats is untouched by design — the sweep counter is
// the driver's own observability surface.
func sweepThumbnailCache(ctx context.Context, cache *thumbnail.Cache, logger *slog.Logger) {
	// The per-pass counter increments on EVERY executed pass (even n == 0):
	// it is the driver's liveness signal — swept_total alone would read zero
	// both when the driver is healthy-but-idle and when it is dead, so the
	// stalled-sweep alert keys off this counter's increase.
	telemetry.IncThumbnailCacheSweepRun(ctx)
	if n := cache.SweepExpired(time.Now()); n > 0 {
		telemetry.IncThumbnailCacheSwept(ctx, int64(n))
		logger.Debug("thumbnail cache sweep removed expired entries", "n", n)
	}
}
