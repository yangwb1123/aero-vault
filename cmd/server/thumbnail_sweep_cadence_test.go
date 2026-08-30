package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/telemetry"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func TestThumbnailCacheSweepPersistsCadence(t *testing.T) {
	ctx := context.Background()
	cache := thumbnail.NewCache(1<<20, time.Second)
	logger := captureLogger(&bytes.Buffer{})
	const cadence = int64(7)

	telemetry.SetThumbnailCacheSweepCadence(ctx, cadence, time.Now().Add(-30*time.Second).Unix())
	for i := 0; i < 3; i++ {
		sweepThumbnailCache(ctx, cache, logger)
	}

	got, ok := cmdScrapeValue(t, "thumbnail_cache_sweep_interval_seconds")
	if !ok {
		t.Fatal("thumbnail_cache_sweep_interval_seconds not found in scrape")
	}
	if got != float64(cadence) {
		t.Fatalf("thumbnail_cache_sweep_interval_seconds = %v, want %d", got, cadence)
	}
}

func TestThumbnailCacheSweepScrapeCadence(t *testing.T) {
	ctx := context.Background()
	cache := thumbnail.NewCache(1<<20, time.Second)
	logger := captureLogger(&bytes.Buffer{})
	oldUnix := time.Now().Add(-30 * time.Second).Unix()

	telemetry.SetThumbnailCacheSweepCadence(ctx, 11, oldUnix)
	sweepThumbnailCache(ctx, cache, logger)

	interval, ok := cmdScrapeValue(t, "thumbnail_cache_sweep_interval_seconds")
	if !ok {
		t.Fatal("thumbnail_cache_sweep_interval_seconds not found in scrape")
	}
	if interval <= 0 {
		t.Fatalf("thumbnail_cache_sweep_interval_seconds = %v, want > 0", interval)
	}

	lastRun, ok := cmdScrapeValue(t, "thumbnail_cache_sweep_last_run_seconds")
	if !ok {
		t.Fatal("thumbnail_cache_sweep_last_run_seconds not found in scrape")
	}
	if lastRun <= float64(oldUnix) {
		t.Fatalf("thumbnail_cache_sweep_last_run_seconds = %v, want > %d", lastRun, oldUnix)
	}
}
