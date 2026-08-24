package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// TestThumbnailSweepInterval pins the pure activation decision (AC1 step 1):
// THUMBNAIL_CACHE_TTL > 0 activates the TTL physical-purge driver in the
// default config (Reconcile.IntervalMinutes == 0), the reconcile arm is
// preserved byte-for-byte, and reconcile wins when both are set (existing
// deployments see zero cadence change).
func TestThumbnailSweepInterval(t *testing.T) {
	cases := []struct {
		name      string
		ttl       int
		reconcile int
		want      time.Duration
	}{
		{"default config, TTL set — the fix", 3600, 0, 3600 * time.Second},
		{"default config, no TTL — no driver", 0, 0, 0},
		{"reconcile set, no TTL — reconcile cadence", 0, 30, 30 * time.Minute},
		{"both set — reconcile wins (existing deployments unchanged)", 3600, 30, 30 * time.Minute},
		{"min TTL — one sweep per second", 1, 0, time.Second},
		{"max TTL — no duration overflow", 31536000, 0, 31536000 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				App:       config.AppConfig{ThumbnailCacheTTL: tc.ttl},
				Reconcile: config.ReconcileCfg{IntervalMinutes: tc.reconcile},
			}
			if got := thumbnailSweepInterval(cfg); got != tc.want {
				t.Fatalf("thumbnailSweepInterval = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestThumbnailSweepActivatesInDefaultConfig proves the fix end to end (AC1
// step 2): under the default reconcile config (IntervalMinutes == 0) with a
// positive TTL, the driver goroutine starts and physically purges an expired
// entry with NO intervening Get — the strongest possible proof that a sweep
// goroutine started. The "started" Info line is written synchronously by
// startThumbnailCacheSweep before it returns, so reading buf here is
// race-free; after cancel() no cache/buf access happens, keeping -race clean.
func TestThumbnailSweepActivatesInDefaultConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := &config.Config{
		App:       config.AppConfig{ThumbnailCacheBytes: 1 << 20, ThumbnailCacheTTL: 1},
		Reconcile: config.ReconcileCfg{IntervalMinutes: 0}, // the default config
	}
	cache := thumbnail.NewCache(cfg.App.ThumbnailCacheBytes, time.Duration(cfg.App.ThumbnailCacheTTL)*time.Second)
	var buf bytes.Buffer
	logger := captureLogger(&buf)

	// The exact production activation shape (main.go): the helper decides, the
	// driver starts.
	if interval := thumbnailSweepInterval(cfg); interval > 0 {
		startThumbnailCacheSweep(ctx, cache, interval, logger)
	} else {
		t.Fatal("thumbnailSweepInterval = 0 in the default config with TTL > 0")
	}
	if !strings.Contains(buf.String(), "thumbnail cache sweep started") {
		t.Fatalf("driver did not start:\n%s", buf.String())
	}

	key := thumbnail.CacheKey{Identity: thumbnail.SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
	payload := make([]byte, 1000)
	cache.Put(key, payload)
	if cache.Len() != 1 || cache.Bytes() != int64(len(payload)) {
		t.Fatalf("entry not stored: len=%d bytes=%d", cache.Len(), cache.Bytes())
	}

	// Never Get the key: removal within TTL + interval + grace is only
	// possible via a driver sweep pass — the strongest proof that a sweep
	// goroutine started under the default reconcile config.
	deadline := time.Now().Add(time.Duration(cfg.App.ThumbnailCacheTTL)*time.Second + time.Second + 100*time.Millisecond)
	for cache.Len() != 0 || cache.Bytes() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("expired entry not physically purged in the default config: len=%d bytes=%d", cache.Len(), cache.Bytes())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Bounded exit: cancel stops the driver at its next select iteration; no
	// cache/buf access after cancel, so -race observes no shared state (the
	// loop's join contract itself is pinned by TestThumbnailCacheSweepDriver).
	cancel()
}

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

	key := thumbnail.CacheKey{Identity: thumbnail.SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
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

	hits, misses, evictions, expired := cache.Stats()
	if hits != 0 || misses != 0 || evictions != 0 || expired != 0 {
		t.Fatalf("Cache.Stats touched by sweep removal: hits=%d misses=%d evictions=%d expired=%d", hits, misses, evictions, expired)
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

// TestThumbnailCacheSweepForwardingLine pins QA P1 #2: the exact production
// forwarding path `if n > 0 { IncThumbnailCacheSwept(...) }` is exercised —
// a TTL cache holding one expired entry, driven through the REAL
// sweepThumbnailCache, must forward n == 1 to the swept counter, and the
// per-pass runs counter must increment on every pass (even n == 0) — the
// SRE F1 liveness signal.
func TestThumbnailCacheSweepForwardingLine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := thumbnail.NewCache(1<<20, 50*time.Millisecond)
	var buf bytes.Buffer
	logger := captureLogger(&buf)

	// Pass 1: empty cache → runs +1, swept +0 (the n==0 arm).
	sweepThumbnailCache(ctx, cache, logger)

	key := thumbnail.CacheKey{Identity: thumbnail.SourceIdentity{TenantID: "t1", Bucket: "bucket", Key: "key", VersionID: "version"}, SourceETag: "e1", EffW: 32, EffH: 32}
	payload := make([]byte, 1000)
	cache.Put(key, payload)
	time.Sleep(60 * time.Millisecond) // let the TTL elapse

	// Pass 2: the expired entry is removed → runs +1, swept +1 (the n>0 arm
	// — the exact line the reviewers flagged as never-executed-under-test).
	sweepThumbnailCache(ctx, cache, logger)
	if cache.Len() != 0 {
		t.Fatalf("sweep did not purge the expired entry: len=%d", cache.Len())
	}
	// The forwarding contract is asserted through the REAL scrape surface:
	// two executed passes → sweep_runs_total delta +2 (the per-pass liveness
	// counter, n==0 arm included), and the one removed entry → swept_total
	// delta +1 (the n>0 arm). A regression that drops either forwarding
	// line now fails here, through the production path.
	runsPre, _ := cmdScrapeValue(t, "thumbnail_cache_sweep_runs_total")
	sweptPre, _ := cmdScrapeValue(t, "thumbnail_cache_swept_total")
	// Pass 3: an empty cache — runs +1, swept +0 (the n==0 arm again, after
	// the scrape baseline).
	sweepThumbnailCache(ctx, cache, logger)
	runsPost, _ := cmdScrapeValue(t, "thumbnail_cache_sweep_runs_total")
	sweptPost, _ := cmdScrapeValue(t, "thumbnail_cache_swept_total")
	if runsPost-runsPre != 1 {
		t.Fatalf("sweep_runs_total delta = %v, want 1 (per-pass forwarding through the production path)", runsPost-runsPre)
	}
	if sweptPost-sweptPre != 0 {
		t.Fatalf("swept_total delta = %v, want 0 (empty pass must not forward a count)", sweptPost-sweptPre)
	}
	if runsPost < 2 {
		t.Fatalf("sweep_runs_total = %v, want >= 2 (the two earlier passes were forwarded)", runsPost)
	}
	if sweptPost < 1 {
		t.Fatalf("swept_total = %v, want >= 1 (the expired-entry pass forwarded its count)", sweptPost)
	}
	_ = payload
}

// TestThumbnailCacheSweepSharedInstanceWiring pins QA P1 #3: the cache
// served by the REST handler is the SAME instance the sweep driver purges —
// the change's core connectivity claim. buildRouter's WithThumbnailCache is
// wired with the caller's pointer, so constructing one cache, passing it to
// buildRouter (REST-serving) and to runThumbnailCacheSweep (purge driver),
// then observing the purge through the REST-served instance proves the
// shared identity end to end.
func TestThumbnailCacheSweepSharedInstanceWiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const ttl = 50 * time.Millisecond
	cache := thumbnail.NewCache(1<<20, ttl)

	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "sweep.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	svc := service.NewFileService(store, repo, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg, err := auth.Parse("k1:default:read+write")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}

	// REST-serving side: rest.NewRouter with the cache-injecting opt — the
	// exact production wiring shape (buildRouter's WithThumbnailCache opt).
	v1 := rest.NewRouter(svc, repo, nil, nil, nil, nil, reg, logger, false, nil, nil, 0, false,
		func(h *rest.Handler) { h.WithThumbnailCache(cache) })
	root := chi.NewRouter()
	root.Mount("/v1", v1)
	// The production middleware chain (main.go): the registry admits the
	// bearer key into the request context before the router's scope gate.
	srv := httptest.NewServer(reg.Middleware()(root))
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/img.png"
	authH := map[string]string{"Authorization": "Bearer k1"}
	if resp, _ := httpPutAuth(u, sweepPNG, "image/png", authH); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	thumb := u + "/thumbnail?w=32&h=32"
	if resp, _ := httpGetAuth(thumb, authH); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	if cache.Len() != 1 {
		t.Fatalf("REST thumbnail did not store into the shared instance: len=%d", cache.Len())
	}

	// Driver side: purge the shared instance; the REST-served entry is gone
	// only after the TTL elapses (the instance is TTL-bound).
	start := time.Now()
	deadline := time.Now().Add(ttl + 3*time.Second)
	for cache.Len() != 0 {
		sweepThumbnailCache(ctx, cache, logger)
		if time.Now().After(deadline) {
			t.Fatalf("shared instance not purged by the driver: len=%d", cache.Len())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if time.Since(start) < ttl {
		t.Fatalf("purge happened before the TTL (%v < %v) — the instance is not TTL-bound", time.Since(start), ttl)
	}
	// After purge, a fresh GET is a miss (runs the pipeline again) — the
	// REST path now observes the purge through the shared instance.
	if resp, _ := httpGetAuth(thumb, authH); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after purge: %d", resp.StatusCode)
	}
	if cache.Len() != 1 {
		t.Fatalf("fresh GET did not re-store into the shared instance: len=%d", cache.Len())
	}
}

// sweepPNG is a minimal decodable PNG fixture (1×1 red) for the wiring test.
var sweepPNG = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}()

func httpGetAuth(url string, auth map[string]string) (*http.Response, []byte) {
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func httpPutAuth(url string, body []byte, contentType string, auth map[string]string) (*http.Response, []byte) {
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}
