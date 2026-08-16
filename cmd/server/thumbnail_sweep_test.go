package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// TestThumbnailCacheSweepDriver proves the TTL physical-purge contract end to
// end (acceptance #1): an entry stored with a TTL and NO intervening Get is
// physically removed (Len/Bytes → 0) within TTL + interval by the timer
// driver — lazy expiry is never the reclaimer here. Step 6 additionally pins
// acceptance #2's "without touching Cache.Stats" at the driver level.
func TestThumbnailCacheSweepDriver(t *testing.T) {
	const (
		ttl      = 100 * time.Millisecond
		interval = 30 * time.Millisecond
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := thumbnail.NewCache(1<<20, ttl)
	var buf bytes.Buffer
	logger := captureLogger(&buf)

	// Initial sweep runs at start while the cache is empty (n=0); the entry
	// below can therefore only be removed by a per-interval sweep.
	done := make(chan struct{})
	go func() {
		runThumbnailCacheSweep(ctx, cache, interval, logger)
		close(done)
	}()

	key := thumbnail.CacheKey{Tenant: "t1", SourceETag: "e1", EffW: 32, EffH: 32}
	payload := make([]byte, 1000)
	cache.Put(key, payload)
	if cache.Len() != 1 || cache.Bytes() != int64(len(payload)) {
		t.Fatalf("entry not stored: len=%d bytes=%d", cache.Len(), cache.Bytes())
	}

	// Never call Get on the key — the entire point: lazy expiry must not be
	// the reclaimer.
	start := time.Now()
	deadline := time.Now().Add(ttl + 3*interval + time.Second)
	for cache.Len() != 0 || cache.Bytes() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("entry not physically purged within deadline: len=%d bytes=%d", cache.Len(), cache.Bytes())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The per-interval sweep that removes the entry runs at most TTL + interval
	// after the Put; the 100 ms grace is a documented anti-flake tolerance.
	if elapsed := time.Since(start); elapsed > ttl+interval+100*time.Millisecond {
		t.Fatalf("entry dropped after %v, want within TTL+interval+grace (~230ms)", elapsed)
	}

	hits, misses, evictions := cache.Stats()
	if hits != 0 || misses != 0 || evictions != 0 {
		t.Fatalf("Cache.Stats touched by sweep removal: hits=%d misses=%d evictions=%d", hits, misses, evictions)
	}

	// No leaked goroutines: the driver must exit on ctx cancel.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("driver goroutine did not exit on cancel")
	}
}

// TestStartThumbnailCacheSweep_Guards pins the driver's defensive gates: a nil
// cache or a zero interval must not start a goroutine or log the started line.
func TestStartThumbnailCacheSweep_Guards(t *testing.T) {
	var buf bytes.Buffer
	logger := captureLogger(&buf)
	ctx := context.Background()
	startThumbnailCacheSweep(ctx, nil, time.Minute, logger)
	startThumbnailCacheSweep(ctx, thumbnail.NewCache(1<<20, time.Second), 0, logger)
	if strings.Contains(buf.String(), "thumbnail cache sweep started") {
		t.Errorf("guarded start logged the started line:\n%s", buf.String())
	}
}
