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
// The module itself enforces the content-MD5 shape precondition on
// sourceETag: only whole-object content-MD5 ETags (exactly 32 lowercase hex,
// ContentMD5ETag) may seed a cache key. A non-content-derived source ETag
// (empty, quoted, uppercase hex, multipart "<md5>-<n>", provider quirks,
// 32-hex-shaped non-MD5 SSE-KMS values) is never a content-identity claim
// and bypasses the cache entirely — no lookup, no store, no counting —
// running the exact shared miss body byte-identically to
// GenerateContextWithOpener with a nil cache.
//
// Ordering is load-bearing: a dead request fast-fails before the shape gate
// (classification: Canceled → silent, DeadlineExceeded → 504); the gate runs
// before key derivation; the cache key is derived BEFORE any slot
// acquisition (from EffectiveDims, the single source of truth shared with
// generateLocked, so the key can never drift from the produced bytes); a
// hit returns the stored JPEG with no slot acquisition, no opener
// invocation, no decode; a miss runs the exact shared slot→open→
// generateLocked→close body; source close is part of generation success,
// and errors are never cached.
func GenerateContextWithOpenerCached(
	ctx context.Context,
	cache *Cache,
	tenant, sourceETag string,
	maxW, maxH int,
	open open3,
) (img []byte, fromCache bool, err error) {
	return generateContextWithOpenerCached(ctx, cache, nil, tenant, sourceETag, maxW, maxH, open)
}

// GenerateContextWithOpenerCachedWithAdmission is the REST-serving variant
// that adds an optional per-tenant decode ceiling. Cache hits still bypass
// admission entirely; misses acquire the tenant slot before the global slot,
// then keep both through open, decode, encode, and stream close.
func GenerateContextWithOpenerCachedWithAdmission(
	ctx context.Context,
	cache *Cache,
	admission *DecodeAdmission,
	tenant, sourceETag string,
	maxW, maxH int,
	open open3,
) (img []byte, fromCache bool, err error) {
	return generateContextWithOpenerCached(ctx, cache, admission, tenant, sourceETag, maxW, maxH, open)
}

func generateContextWithOpenerCached(
	ctx context.Context,
	cache *Cache,
	admission *DecodeAdmission,
	tenant, sourceETag string,
	maxW, maxH int,
	open open3,
) (img []byte, fromCache bool, err error) {
	if open == nil {
		return nil, false, errors.New("thumbnail: GenerateContextWithOpenerCached: nil opener")
	}

	// Fast-fail dead requests before any work, mirroring generateLocked's
	// terminal checks and acquireDecodeSlotContext's re-check. This stays
	// BEFORE the shape gate: a dead request returns ctx.Err() regardless of
	// ETag shape, preserving the handler's classification contract
	// (Canceled → silent, DeadlineExceeded → 504). The gate adds no
	// observable behavior for dead requests.
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	// Shape gate: only whole-object content-MD5 ETags (exactly 32 lowercase
	// hex, ContentMD5ETag) may seed a cache key — the module enforces its
	// own documented precondition instead of importing it from an adapter. A
	// non-content-derived source ETag is never a content-identity claim: run
	// the exact shared miss body (slot → open → *OpenError wrap → close →
	// generateLocked) plus the existing empty-result defensive check, and
	// return — no lookup, no store, zero telemetry, byte-identical to
	// GenerateContextWithOpener with a nil cache.
	if !ContentMD5ETag(sourceETag) {
		if cache != nil && cache.Enabled() {
			telemetry.IncThumbnailCacheBypass(ctx, "non-content-md5")
		}
		img, _, err := generateContextWithAdmission(ctx, maxW, maxH, admission, tenant, open)
		if err != nil {
			return nil, false, err
		}
		if len(img) == 0 {
			return nil, false, errors.New("thumbnail: GenerateContextWithOpenerCached: empty result")
		}
		return img, false, nil
	}

	// Cache key derivation. Ordering is load-bearing: the key is derived
	// BEFORE any slot acquisition (from EffectiveDims, the single source of
	// truth shared with generateLocked, so the key can never drift from the
	// produced bytes); a hit returns the stored JPEG with no slot
	// acquisition, no opener invocation, no decode.
	effW, effH := EffectiveDims(maxW, maxH)
	key := CacheKey{Tenant: tenant, SourceETag: sourceETag, EffW: effW, EffH: effH, Version: CacheKeyVersion}
	// Hit path: no slot acquisition, no opener invocation, no decode. The
	// disabled cache (NewCache(<=0)) and a nil cache both skip the lookup and
	// count nothing (nil/disabled = byte-identical to today).
	if cached, hit, lerr := lookupCached(ctx, cache, key); lerr != nil {
		return nil, false, lerr
	} else if hit {
		return cached, true, nil
	}

	// Miss path: the exact shared body of GenerateContextWithOpener (slot →
	// open → *OpenError wrap → generateLocked → close), byte-identical. A
	// close failure is an error, so the cache gate below is never reached.
	img, openedETag, err := generateContextWithAdmission(ctx, maxW, maxH, admission, tenant, open)
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
// and returns the stored bytes. On a genuine miss it counts the miss; on a
// TTL-expired read it counts the expired class (thumbnail.cache.expired_total
// — never the hit-ratio miss class) and returns hit=false exactly like a
// miss, so the caller runs the full miss body and re-stores with a fresh
// expiry. A nil/disabled cache is a miss that counts nothing.
func lookupCached(ctx context.Context, cache *Cache, key CacheKey) (img []byte, hit bool, err error) {
	if cache == nil || cache.disabled {
		return nil, false, nil
	}
	img, outcome := cache.Get(key)
	switch outcome {
	case GetExpired:
		telemetry.IncThumbnailCacheExpired(ctx)
		return nil, false, nil
	case GetMiss:
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
	if !cache.PayloadFits(img) {
		telemetry.IncThumbnailCacheBypass(ctx, "store-refused")
	}
	if n := cache.Put(key, img); n > 0 {
		telemetry.IncThumbnailCacheEviction(ctx, int64(n))
	}
}
