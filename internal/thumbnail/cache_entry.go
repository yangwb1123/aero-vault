package thumbnail

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// GenerateContextWithOpenerCached is GenerateContextWithOpener with a
// server-side output cache (key = tenant, source ETag, effective dims). The
// opener additionally reports the opened object's source ETag so the store
// rule can verify content identity before caching (a concurrent PUT between
// the caller's Stat and the open must never store new-version bytes under an
// old-version key). fromCache reports whether the bytes came from the cache
// (the handler uses it for EventAccessed parity on the hit path — misses emit
// on stream open inside the service, hits bypass the stream). A nil cache or
// a zero-budget cache behaves exactly like GenerateContextWithOpener: opener
// invoked once per call, identical slot lifecycle, byte-identical output.
//
// Ordering is load-bearing: the cache key is derived BEFORE any slot
// acquisition (from EffectiveDims, the single source of truth shared with
// generateLocked, so the key can never drift from the produced bytes); a
// dead request fast-fails before the lookup; a hit returns the stored JPEG
// with no slot acquisition, no opener invocation, no decode; a miss runs the
// exact shared slot→open→generateLocked body; errors are never cached.
func GenerateContextWithOpenerCached(
	ctx context.Context,
	cache *Cache,
	tenant, sourceETag string,
	maxW, maxH int,
	open open3,
) (img []byte, fromCache bool, err error) {
	if open == nil {
		return nil, false, errors.New("thumbnail: GenerateContextWithOpenerCached: nil opener")
	}
	effW, effH := EffectiveDims(maxW, maxH)
	key := CacheKey{Tenant: tenant, SourceETag: sourceETag, EffW: effW, EffH: effH}

	// Fast-fail dead requests before any work, mirroring generateLocked's
	// terminal checks and acquireDecodeSlotContext's re-check.
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	// Hit path: no slot acquisition, no opener invocation, no decode. The
	// disabled cache (NewCache(<=0)) and a nil cache both skip the lookup and
	// count nothing (nil/disabled = byte-identical to today).
	if cached, hit, lerr := lookupCached(ctx, cache, key); lerr != nil {
		return nil, false, lerr
	} else if hit {
		return cached, true, nil
	}

	// Miss path: the exact shared body of GenerateContextWithOpener (slot →
	// open → *OpenError wrap → close → generateLocked), byte-identical.
	img, openedETag, err := generateContextWithOpener3(ctx, maxW, maxH, open)
	if err != nil {
		// Errors are never cached; the sentinel/classification surface is
		// preserved byte-for-byte.
		return nil, false, err
	}
	if len(img) == 0 {
		// Defensive: an empty success is never stored and never served as an
		// empty 200 (generateLocked returns a non-empty buffer on success;
		// unreachable in practice).
		return nil, false, errors.New("thumbnail: GenerateContextWithOpenerCached: empty result")
	}
	storeCached(ctx, cache, key, sourceETag, openedETag, img)
	return img, false, nil
}

// lookupCached consults the cache and reports whether the caller should
// short-circuit. On a hit it re-checks ctx (a request that went dead while
// the cache lock was held must not receive cached bytes — the handler's
// classification, Canceled → silent / DeadlineExceeded → 504, is preserved)
// and returns the stored bytes. On a miss it counts the miss and returns
// hit=false; a nil/disabled cache is a miss that counts nothing.
func lookupCached(ctx context.Context, cache *Cache, key CacheKey) (img []byte, hit bool, err error) {
	if cache == nil || cache.disabled {
		return nil, false, nil
	}
	img, hit = cache.Get(key)
	if !hit {
		telemetry.IncThumbnailCacheMiss(ctx)
		return nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	telemetry.IncThumbnailCacheHit(ctx)
	return img, true, nil
}

// storeCached applies the ETag-verify-before-Put store rule: only a
// successful generation whose opened ETag matches the key's source ETag is
// cached. A mismatch (a PUT landed between the caller's Stat and the open)
// skips the store — the bytes are still returned (the handler's
// opened-derived validator truthfully describes them) but never under a
// stale key. Evictions from byte-budget pressure are forwarded to telemetry.
func storeCached(ctx context.Context, cache *Cache, key CacheKey, sourceETag, openedETag string, img []byte) {
	if cache == nil || cache.disabled || openedETag == "" || openedETag != sourceETag {
		return
	}
	if n := cache.Put(key, img); n > 0 {
		telemetry.IncThumbnailCacheEviction(ctx, int64(n))
	}
}
