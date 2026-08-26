package thumbnail

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// CachedGenerationResult includes the source metadata needed by protocol
// adapters when a successful result was produced by another flight caller.
// Opened is valid when HasOpened is true; resident cache hits leave it false.
type CachedGenerationResult struct {
	Image     []byte
	FromCache bool
	Opened    OpenedSource
	HasOpened bool
}

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

// GenerateContextWithOpenerCachedWithAdmissionResult is the metadata-preserving
// form used by adapters that must construct a response from a coalesced
// leader's opened representation.
func GenerateContextWithOpenerCachedWithAdmissionResult(
	ctx context.Context,
	cache *Cache,
	admission *DecodeAdmission,
	identity SourceIdentity,
	sourceETag string,
	maxW, maxH int,
	open Opener,
) (CachedGenerationResult, error) {
	return generateContextWithOpenerCachedResult(ctx, cache, admission, identity, sourceETag, maxW, maxH, open)
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
	result, err := generateContextWithOpenerCachedResult(ctx, cache, admission, identity, sourceETag, maxW, maxH, open)
	return result.Image, result.FromCache, err
}

func generateContextWithOpenerCachedResult(
	ctx context.Context,
	cache *Cache,
	admission *DecodeAdmission,
	identity SourceIdentity,
	sourceETag string,
	maxW, maxH int,
	open Opener,
) (result CachedGenerationResult, err error) {
	if open == nil {
		return result, errors.New("thumbnail: GenerateContextWithOpenerCached: nil opener")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !identity.Complete() {
		result.Image, result.FromCache, err = generateUncached(ctx, admission, identity.TenantID, maxW, maxH, open)
		return result, err
	}
	if !ContentMD5ETag(sourceETag) {
		if cache != nil && cache.Enabled() {
			telemetry.IncThumbnailCacheBypass(ctx, "non-content-md5")
		}
		result.Image, result.FromCache, err = generateUncached(ctx, admission, identity.TenantID, maxW, maxH, open)
		return result, err
	}
	effW, effH := EffectiveDims(maxW, maxH)
	key := CacheKey{
		Identity: identity, SourceETag: sourceETag,
		EffW: effW, EffH: effH, Version: CacheKeyVersion,
		Representation: currentRepresentationToken,
	}
	if cached, hit, lookupErr := lookupCached(ctx, cache, key); lookupErr != nil {
		return result, lookupErr
	} else if hit {
		return CachedGenerationResult{Image: cached, FromCache: true}, nil
	}
	flight, leader := beginFlight(cache, key)
	if flight == nil {
		return generateMiss(ctx, cache, admission, identity, sourceETag, key, maxW, maxH, open)
	}
	if !leader {
		return waitFlightResult(ctx, flight)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			finishFlight(cache, key, flight, CachedGenerationResult{}, errCoalescedLeaderPanic)
			panic(recovered)
		}
		finishFlight(cache, key, flight, result, err)
	}()
	// A flight may have completed between the caller's first lookup and
	// beginFlight. Recheck while owning the new flight so a late caller does
	// not start duplicate work after a previous leader stored the result.
	// This observational peek deliberately does not count a second miss or
	// mutate LRU hit state.
	if cached, hit, lookupErr := cache.peekContext(ctx, key); lookupErr != nil {
		return result, lookupErr
	} else if hit {
		return CachedGenerationResult{Image: cached, FromCache: true}, nil
	}
	return generateMiss(ctx, cache, admission, identity, sourceETag, key, maxW, maxH, open)
}

func generateMiss(
	ctx context.Context,
	cache *Cache,
	admission *DecodeAdmission,
	identity SourceIdentity,
	sourceETag string,
	key CacheKey,
	maxW, maxH int,
	open Opener,
) (CachedGenerationResult, error) {
	img, opened, err := generateContextWithAdmission(ctx, maxW, maxH, admission, identity.TenantID, open)
	if err != nil {
		return CachedGenerationResult{}, err
	}
	if len(img) == 0 {
		return CachedGenerationResult{}, errors.New("thumbnail: GenerateContextWithOpenerCached: empty result")
	}
	storeCached(ctx, cache, key, identity, sourceETag, opened, img)
	return CachedGenerationResult{Image: img, Opened: opened, HasOpened: true}, nil
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
