package telemetry

import (
	"context"
	"testing"
)

func TestSetThumbnailCacheSweepCadencePreservesPriorIntervalOnNonPositive(t *testing.T) {
	if sharedPromHandler == nil {
		t.Skip("sharedPromHandler not initialized")
	}
	ctx := context.Background()

	SetThumbnailCacheSweepCadence(ctx, 13, 41)
	body := scrapeShared(t)
	if v, ok := scrapeValue(body, "thumbnail_cache_sweep_interval_seconds"); !ok || v != 13 {
		t.Fatalf("interval after positive record: value=%v ok=%v want 13", v, ok)
	}
	if v, ok := scrapeValue(body, "thumbnail_cache_sweep_last_run_seconds"); !ok || v != 41 {
		t.Fatalf("last_run after positive record: value=%v ok=%v want 41", v, ok)
	}

	SetThumbnailCacheSweepCadence(ctx, 0, 99)
	body = scrapeShared(t)
	if v, ok := scrapeValue(body, "thumbnail_cache_sweep_interval_seconds"); !ok || v != 13 {
		t.Fatalf("interval after zero sentinel: value=%v ok=%v want 13", v, ok)
	}
	if v, ok := scrapeValue(body, "thumbnail_cache_sweep_last_run_seconds"); !ok || v != 99 {
		t.Fatalf("last_run after zero sentinel: value=%v ok=%v want 99", v, ok)
	}

	SetThumbnailCacheSweepCadence(ctx, -5, 123)
	body = scrapeShared(t)
	if v, ok := scrapeValue(body, "thumbnail_cache_sweep_interval_seconds"); !ok || v != 13 {
		t.Fatalf("interval after negative sentinel: value=%v ok=%v want 13", v, ok)
	}
	if v, ok := scrapeValue(body, "thumbnail_cache_sweep_last_run_seconds"); !ok || v != 123 {
		t.Fatalf("last_run after negative sentinel: value=%v ok=%v want 123", v, ok)
	}
}
