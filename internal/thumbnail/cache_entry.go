package thumbnail

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// GenerateContextWithOpenerCached generates a thumbnail with an identity-bound
// output cache. Incomplete identities bypass both lookup and storage.
func GenerateContextWithOpenerCached(
	ctx context.Context,
	cache *Cache,
	identity SourceIdentity,
	sourceETag string,
	maxW, maxH int,
	open Opener,
) ([]byte, bool, error) {
	return generateContextWithOpenerCached(ctx, cache, nil, identity, sourceETag, maxW, maxH, open)
}

// GenerateContextWithOpenerCachedWithAdmission adds the optional per-tenant
// decode ceiling. Cache hits still avoid admission, opening, and decoding.
func GenerateContextWithOpenerCachedWithAdmission(
	ctx context.Context,
	cache *Cache,
	admission *DecodeAdmission,
	identity SourceIdentity,
	sourceETag string,
	maxW, maxH int,
	open Opener,
) ([]byte, bool, error) {
	return generateContextWithOpenerCached(ctx, cache, admission, identity, sourceETag, maxW, maxH, open)
}

func generateContextWithOpenerCached(
	ctx context.Context,
	cache *Cache,
	admission *DecodeAdmission,
	identity SourceIdentity,
	sourceETag string,
	maxW, maxH int,
	open Opener,
) ([]byte, bool, error) {
	if open == nil {
		return nil, false, errors.New("thumbnail: GenerateContextWithOpenerCached: nil opener")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !identity.Complete() {
		return generateUncached(ctx, admission, identity.TenantID, maxW, maxH, open)
	}
	if !ContentMD5ETag(sourceETag) {
		if cache != nil && cache.Enabled() {
			telemetry.IncThumbnailCacheBypass(ctx, "non-content-md5")
		}
		return generateUncached(ctx, admission, identity.TenantID, maxW, maxH, open)
	}
	effW, effH := EffectiveDims(maxW, maxH)
	key := CacheKey{
		Identity: identity, SourceETag: sourceETag,
		EffW: effW, EffH: effH, Version: CacheKeyVersion,
	}
	if cached, hit, err := lookupCached(ctx, cache, key); err != nil {
		return nil, false, err
	} else if hit {
		return cached, true, nil
	}
	img, opened, err := generateContextWithAdmission(ctx, maxW, maxH, admission, identity.TenantID, open)
	if err != nil {
		return nil, false, err
	}
	if len(img) == 0 {
		return nil, false, errors.New("thumbnail: GenerateContextWithOpenerCached: empty result")
	}
	storeCached(ctx, cache, key, identity, sourceETag, opened, img)
	return img, false, nil
}

func generateUncached(
	ctx context.Context, admission *DecodeAdmission, tenant string, maxW, maxH int, open Opener,
) ([]byte, bool, error) {
	img, _, err := generateContextWithAdmission(ctx, maxW, maxH, admission, tenant, open)
	if err != nil {
		return nil, false, err
	}
	if len(img) == 0 {
		return nil, false, errors.New("thumbnail: GenerateContextWithOpenerCached: empty result")
	}
	return img, false, nil
}

func lookupCached(ctx context.Context, cache *Cache, key CacheKey) ([]byte, bool, error) {
	if cache == nil || cache.disabled || !key.Identity.Complete() {
		return nil, false, nil
	}
	img, outcome, err := cache.getContext(ctx, key)
	if err != nil {
		return nil, false, err
	}
	switch outcome {
	case GetExpired:
		telemetry.IncThumbnailCacheExpired(ctx)
		return nil, false, nil
	case GetMiss:
		telemetry.IncThumbnailCacheMiss(ctx)
		return nil, false, nil
	default:
		telemetry.IncThumbnailCacheHit(ctx)
		return img, true, nil
	}
}

// storeCached requires both the authoritative identity and a storage proof for
// the opened descriptor. An opener may return bytes normally after a race, but
// those bytes are never admitted under the pre-open cache key.
func storeCached(
	ctx context.Context,
	cache *Cache,
	key CacheKey,
	identity SourceIdentity,
	sourceETag string,
	opened OpenedSource,
	img []byte,
) {
	if cache == nil || cache.disabled || !key.Identity.Complete() ||
		!key.Identity.Equal(identity) || key.SourceETag != sourceETag ||
		!opened.Bound || !opened.Identity.Equal(identity) || opened.ETag != sourceETag {
		return
	}
	if !cache.PayloadFits(img) {
		telemetry.IncThumbnailCacheBypass(ctx, "store-refused")
	}
	if n := cache.Put(key, img); n > 0 {
		telemetry.IncThumbnailCacheEviction(ctx, int64(n))
	}
}
