package rest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/telemetry"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// GET /v1/files/<key>/thumbnail?w=&h=&version=
// Generates a JPEG thumbnail of an image object on demand. The response carries
// a safe identity-bound derived ETag so repeat requests are cacheable (304 via
// If-None-Match).
//
// Dispatch precedence: object keys ending in "/thumbnail" are legal. When an
// object exists at the exact requested key, raw-download semantics win (FR-1)
// and the subresource interpretation applies only when no object exists at
// the full key (FR-2). A ?version= pin requests a specific historical
// version: when the pinned version names a readable object at the full key,
// raw-download semantics win there too (a version-pinned read must never be
// shadowed by derived content of a different key); otherwise the thumbnail is
// derived from the pinned version of the trimmed key.
func (h *Handler) Thumbnail(w http.ResponseWriter, r *http.Request) {
	fullKey := keyFromPath(r)
	version := ""
	if r.URL.Query().Has("version") {
		// Pinned arm: the discriminator is a pinned-version lookup at the
		// FULL key — never a current-object Stat. A soft-deleted full key
		// returns ErrNotFound from a current Stat while its pre-delete
		// versions remain readable, and a version-pinned read of such an
		// object must serve its own raw bytes. The lookup is a repo read plus
		// an authorization decision (StatVersionWithOptions runs
		// authorizeObject — the E7 discriminator is an authz decision, not a
		// pure repo read) — no stream, no decode slot — run on the original
		// request context, before the thumbnail deadline scope (F1 parity).
		version = r.URL.Query().Get("version") // "" resolves the current object (Get parity)
		_, err := h.svc.StatVersionWithOptions(r.Context(), mw.TenantFrom(r.Context()),
			service.DefaultBucket, fullKey, version, service.ReadOptions{})
		switch {
		case err == nil:
			// The pin names a readable object at the full key: delegate to
			// Get, which re-resolves ?version= via GetSpecificVersion with
			// full raw-download semantics (policy → anonymous → conditional
			// → Range → X-Version-Id).
			h.Get(w, r)
			return
		case errors.Is(err, service.ErrForbidden):
			// Re-delegate: Get re-authorizes after allowAnonymous injects
			// the canned-public-read capability, so anonymous reads of
			// public exact-key pinned versions keep working.
			h.Get(w, r)
			return
		case errors.Is(err, service.ErrNotFound):
			// The pin names nothing at the full key: fall through to the
			// version-pinned derivation on the trimmed key.
		case errors.Is(err, service.ErrInvalidArgs) && len(fullKey) > service.MaxKeyLen:
			// Key-length dispatch artifact: a legal image key whose
			// "/thumbnail" suffix exceeds the key-length cap is a dispatch
			// artifact, not an object-state error — fall through (mirrors
			// the unpinned ErrInvalidArgs arm). Any other ErrInvalidArgs
			// (e.g. an SSE-C object read without its key) lands in default:
			// the pin names a real but unreadable object, and deriving a
			// different key's content would shadow it.
		default:
			// The pinned full-key version exists in a real but unreadable
			// state (corrupt → 410, SSE-C without key → 400): never fall
			// back to derived content of a different key (FR-3).
			h.writeError(w, r, err)
			return
		}
	} else {
		_, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, fullKey)
		switch {
		case err == nil:
			// The exact key names a real object: raw download wins with full
			// Get semantics (bucket policy, anonymous gate on the full key,
			// conditional requests, Range, ?version=).
			h.Get(w, r)
			return
		case errors.Is(err, service.ErrNotFound):
			// No object at the full key: fall through to the subresource
			// interpretation below.
		case errors.Is(err, service.ErrForbidden):
			// The full key names an existing object the caller cannot read with
			// the pre-capability principal. Delegate to Get, which re-authorizes
			// after allowAnonymous injects the canned-public-read capability, so
			// anonymous reads of public objects keep working in access-enabled
			// deployments; literal propagation would regress them to 403.
			h.Get(w, r)
			return
		case errors.Is(err, service.ErrInvalidArgs):
			// A legal object key suffixed with "/thumbnail" can exceed the
			// key-length cap (e.g. a 191-char image key + 10-char suffix). That
			// is a dispatch artifact, not an object-state error: fall through to
			// the subresource interpretation, which works on the trimmed
			// (legal) key.
		default:
			// The key names a real object; propagate corrupt/other errors instead
			// of falling back to derived content of a different key (FR-3).
			h.writeError(w, r, err)
			return
		}
	}
	key := strings.TrimSuffix(fullKey, "/thumbnail")
	// Bucket-policy gate on the derivation path, mirroring Get's ordering
	// (policy → anonymous): a policy denying s3:GetObject on the trimmed key
	// must not be bypassable by requesting its derived thumbnail (which
	// returns near-lossless image bytes of the same object). Fail-closed like
	// every other object read surface — a version-pinned URL cannot bypass it
	// either. Run before the deadline scope: the gates are cheap and
	// fail-closed either way.
	if !h.checkBucketPolicy(w, r, key, "s3:GetObject") {
		return
	}
	if !h.allowAnonymous(w, r, key) {
		return
	}
	// Server-side bound on the derivation pipeline, including the decode-slot
	// park: REQUEST_TIMEOUT_SECONDS (same knob as the AI group) cancels a
	// scoped context that GenerateContext honors while parked. Scoped HERE —
	// after the delegation checks and gates — so the arms above that
	// raw-download "/thumbnail"-suffixed keys (?version= pin, exact-key,
	// ErrForbidden) are never collateralized by the thumbnail deadline (F1).
	// No-op at 0.
	if h.thumbnailTimeout > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), h.thumbnailTimeout)
		defer cancel()
		r = r.WithContext(ctx)
	}
	h.thumbnailDerive(w, r, key, version)
}

// thumbnailDerive is the shared derivation body for both dispatch arms,
// parameterized by the source key and the optional version pin ("" = the
// current object, byte-identical to the historical unpinned behavior). The
// pinned substitutions are localized in three places — the pre-open Stat and
// the 304 re-Stat go through statPinned, and the opener selects GetVersion
// instead of Get; everything else (media gate, dims, validator shape, cache
// admission, classification) is one code path serving both arms.
func (h *Handler) thumbnailDerive(w http.ResponseWriter, r *http.Request, key, version string) {
	tenant := mw.TenantFrom(r.Context())
	obj, err := h.statPinned(r.Context(), tenant, key, version)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// Media-type gate, four-bucket partition of the normalized declared
	// Content-Type (RFC 9110 §8.3.1: case-insensitive, parameters stripped —
	// so "Image/JPEG" and "image/jpeg; charset=utf-8" both normalize to
	// image/jpeg and pass). For a pinned request the gate describes the
	// PINNED version's bytes — a pinned text/plain version must 400 even if
	// the current version is image/png:
	//   1. image/jpeg|image/png|image/gif   → proceed (declared decodable)
	//   2. other image/* (e.g. image/webp)  → 415 (server capability)
	//   3. absent/unparseable/octet-stream  → byte-decided at open (Sniff)
	//   4. other non-image non-generic      → 400 (client argument)
	// Buckets 1/2/4 keep their historical statuses and messages verbatim;
	// only bucket 3 changes — the curl -T / S3-SDK upload norm stores an
	// empty or generic declaration on perfectly decodable JPEG/PNG/GIF
	// bytes, which the pipeline can classify from magic at open time.
	mediaType, _, perr := mime.ParseMediaType(obj.ContentType)
	sniffBytes := perr != nil || mediaType == "application/octet-stream"
	if !sniffBytes && !strings.HasPrefix(mediaType, "image/") {
		h.writeThumbnailError(w, r, fmt.Errorf("%w: object is not an image (content-type %q)", service.ErrInvalidArgs, obj.ContentType))
		return
	}
	if !sniffBytes {
		switch mediaType {
		case "image/jpeg", "image/png", "image/gif":
		default:
			h.writeThumbnailError(w, r, fmt.Errorf("%w: unsupported image format %q (supported: image/jpeg, image/png, image/gif)",
				thumbnail.ErrUnsupportedFormat, obj.ContentType))
			return
		}
	}
	// Validate ?w=/?h= before the ETag derivation, the If-None-Match/304
	// branch, the object-stream open, and the decode pipeline: garbage
	// dimensions are client argument errors (400) and must not produce a
	// silently-defaulted 200 whose garbage-derived ETag pollutes shared
	// caches.
	q := r.URL.Query()
	maxW, err := parseThumbDim(q, "w")
	if err != nil {
		h.writeThumbnailError(w, r, err)
		return
	}
	maxH, err := parseThumbDim(q, "h")
	if err != nil {
		h.writeThumbnailError(w, r, err)
		return
	}

	// Validator derived from the EFFECTIVE dimensions the pipeline applies
	// (generateLocked: <=0 → DefaultMax, >HardMax → HardMax), not the raw
	// ?w=/?h= values: requests whose dims differ only in clamped-away values
	// (?w=2048 vs ?w=9999; ?w=0&h=0 vs absent) produce byte-identical JPEGs
	// and must share one validator — or shared caches fragment entries and
	// re-run the full pipeline per distinct oversized URL instead of the 304
	// fast path. Raw values still flow to GenerateContextWithOpener; the
	// re-clamp is idempotent, so output bytes are unchanged.
	effW, effH := thumbnail.EffectiveDims(maxW, maxH)
	identity := thumbnailSourceIdentity(obj)
	sourceBound := identity.Complete() && h.svc.ThumbnailSourceCacheBound(obj)
	statETag := ""
	if sourceBound {
		// A reusable strong validator requires both complete repository
		// identity and a backend proof that the opened descriptor belongs to
		// that generation. Unsupported or legacy backends still generate a
		// response, but must not certify a repository identity that was never
		// bound to the returned bytes.
		statETag = thumbValidatorETag(thumbnail.CacheKeyVersion, identity, obj.ETag, effW, effH)
	}
	// Strong read preconditions run before cache lookup, slot acquisition, open,
	// or the conditional re-stat. A complete identity still provides a safe
	// opaque validator on an uncached source; incomplete identities fail closed
	// for a specific If-Match token while preserving wildcard/date semantics.
	if readPreconditionFailedForETag(r, obj, statETag) {
		h.writeError(w, r, service.ErrPreconditionFailed)
		return
	}
	// Shared-cache directive: public only for an anonymous public-read request;
	// authenticated requests retain the private behavior.
	cacheControl := fmt.Sprintf("private, max-age=%d, must-revalidate", thumbFreshnessMaxAge)
	if auth.IsAnonymous(r.Context()) {
		cacheControl = fmt.Sprintf("public, max-age=%d, must-revalidate", thumbFreshnessMaxAge)
	}
	addThumbnailVary(w)
	// Unpinned X-Version-Id emission needs the bucket versioning gate (S3 parity).
	versioning := version != "" || h.bucketVersioning(r.Context(), tenant)
	// Conditional re-observation gate: an INM match OR an IMS-only request
	// (no INM header) enters the re-Stat block — the IMS arm needs the fresh
	// observation to evaluate the date comparison (RFC 9110 §13).
	if statETag != "" && (etagListMatches(r.Header.Get("If-None-Match"), statETag) ||
		(r.Header.Get("If-None-Match") == "" && r.Header.Get("If-Modified-Since") != "")) {
		// Re-observe the object before certifying Not Modified. The pre-open
		// Stat above and this emission are separate moments: a concurrent PUT
		// between them would otherwise pair a stale validator with 304, and a
		// shared cache holding the OLD derived thumbnail would keep serving it
		// (Cache-Control public|private, max-age=300, must-revalidate) until
		// the bounded window lapses — no subsequent PUT invalidates the
		// derived resource. The re-Stat is a repository read plus an
		// authorization decision (StatVersionWithOptions runs
		// authorizeObject — the E7 discriminator is an authz decision, not a
		// pure repo read) — no stream, no decode slot — so the fast path stays
		// slot-free and stream-free. The 304's validator and Last-Modified are
		// pinned to THIS observation (RFC 9110 §13.1.2: conditions evaluate
		// against the current validator). For pinned requests the re-Stat is
		// deterministic (version rows are immutable) but the shared path is
		// kept, not special-cased.
		fresh, err := h.statPinned(r.Context(), tenant, key, version)
		if err != nil {
			// Deleted between the Stats (ErrNotFound → 404) or corrupt (→ 410):
			// never certify Not Modified for a state we could not observe. Same
			// writeError classification as the pre-check Stat.
			h.writeError(w, r, err)
			return
		}
		freshIdentity := thumbnailSourceIdentity(fresh)
		freshETag := ""
		// Generation proof gates both reusable caching and validators. An
		// unsupported backend cannot safely certify a 304 because its bytes
		// are not bound to the repository generation.
		if sourceBound && freshIdentity.Complete() && h.svc.ThumbnailSourceCacheBound(fresh) {
			freshETag = thumbValidatorETag(thumbnail.CacheKeyVersion, freshIdentity, fresh.ETag, effW, effH)
		}
		if readPreconditionFailedForETag(r, fresh, freshETag) {
			h.writeError(w, r, service.ErrPreconditionFailed)
			return
		}
		// Conditional evaluation per RFC 9110 §13: If-None-Match takes
		// precedence when present; otherwise If-Modified-Since is evaluated
		// against the re-observed Last-Modified.
		inmHit := freshETag != "" && etagListMatches(r.Header.Get("If-None-Match"), freshETag)
		imsHit := false
		if r.Header.Get("If-None-Match") == "" {
			if ims := r.Header.Get("If-Modified-Since"); ims != "" {
				if t, perr := http.ParseTime(ims); perr == nil {
					imsHit = !fresh.UpdatedAt.Truncate(time.Second).After(t)
				}
			}
		}
		if freshETag != "" && (inmHit || imsHit) {
			if (version != "" || versioning) && fresh.VersionID != "" {
				w.Header().Set("X-Version-Id", fresh.VersionID)
			}
			setThumbnailETag(w, freshETag)
			w.Header().Set("Last-Modified", fresh.UpdatedAt.UTC().Format(http.TimeFormat))
			// The 304 must mirror the 200's directive (RFC 9110 §15.4.5: a
			// 304 MUST carry the Cache-Control the 200 would have sent;
			// RFC 9111 §4.3.4: the 304's header fields update the stored
			// response, so a divergent directive would rewrite the stored
			// freshness; the public/private split itself is per RFC 9111
			// §3.5). A shared cache
			// revalidating a private thumb keeps the bounded directive
			// (thumbFreshnessMaxAge + must-revalidate), and one holding the
			// anonymous public entry cannot have its stored directive
			// rewritten to private by an authenticated 304.
			w.Header().Set("Cache-Control", cacheControl)
			// Revalidation observability: bounded client freshness converts
			// silent cache hits into these conditionals (per client per URL
			// per window); each certified 304 costs three repo point reads
			// (dispatch Stat, pre-open Stat, re-Stat) — no stream, no decode
			// slot.
			telemetry.IncThumbnail304(r.Context())
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// The object changed between the two Stats (the fresh validator no
		// longer matches the client's If-None-Match): fall through to the 200
		// path, which re-derives the validator from the opened object and
		// serves the current bytes — never a stale-validator 304.
	}

	// The decode slot is acquired BEFORE the object stream opens —
	// GenerateContextWithOpenerCached acquires, then invokes the opener
	// (svc.Get), then decodes — so at most maxConcurrentDecodes object
	// streams are open at once, and a request parked on the semaphore holds
	// no stream at all. The server-side cache (THUMBNAIL_CACHE_BYTES; keyed
	// by complete source identity + source ETag + effective dims + schema version) is consulted
	// after full authorization and the 304 fast path; a hit returns the
	// stored JPEG with NO slot, NO opener, NO decode (it also emits the
	// access event below so every 200 emits exactly one EventAccessed). Open
	// failures surface as *OpenError and keep today's writeError
	// classification verbatim; decode and context errors keep the branch
	// below. The stream lifecycle (close on every path, close-before-
	// release) lives inside the API. Three admission gates bypass the cache
	// by design (pre-launch conditions):
	//   - SSE-C objects (SSECustomerInfo ok): the server never holds bytes
	//     derived from customer-keyed decryption beyond the request —
	//     caching the derived JPEG would persist them indefinitely in
	//     server memory.
	//   - SSE-KMS objects (ServerSideEncryptionInfo reports aws:kms): AWS
	//     documents SSE-KMS ETags as non-MD5 — they may even be 32-hex-
	//     shaped — the metadata gate, not shape, excludes them.
	//   - any other source ETag not exactly 32 lowercase hex (multipart
	//     "<md5>-<n>" contains a dash; OSS/COS provider quirks): the ETag
	//     is not content-derived, so the key could pair stale bytes with a
	//     legitimately changed object; only whole-object content MD5 ETags
	//     admit caching.
	// For a pinned request all gates evaluate the PINNED version's metadata
	// and ETag (the lookup key carries its ETag; no CacheKeyVersion bump).
	cache := h.thumbnailCache
	if !sourceBound {
		recordThumbnailCacheBypass(r.Context(), cache, "storage-generation")
		cache = nil
	}
	if reason := thumbnailCacheBypassReason(obj); reason != "" {
		recordThumbnailCacheBypass(r.Context(), cache, reason)
		if reason == "sse-c" || reason == "sse-kms" {
			cache = nil
		}
	}
	// The 200-path validator must describe the bytes actually decoded, so
	// the opened object is captured here and read after the pipeline
	// succeeds. Same capture-then-serve ordering the Get handler uses
	// (handler.go -> handleRangeOrFull): the pre-open Stat validator serves
	// only the 304 fast path, never the 200. The opener also reports the
	// opened object's ETag so the cache's store rule can verify content
	// identity before caching (a PUT between the Stat and the open never
	// caches new-version bytes under an old-version key; for pins the ETag
	// is immutable, so the rule is trivially satisfied and remains the
	// residual guard).
	var opened *service.ThumbnailSource
	img, fromCache, err := thumbnail.GenerateContextWithOpenerCachedWithAdmission(
		r.Context(), cache, h.thumbnailAdmission, identity, obj.ETag, maxW, maxH,
		func() (io.ReadCloser, thumbnail.OpenedSource, error) {
			source, err := h.svc.OpenThumbnailSource(r.Context(), obj, version)
			if err != nil {
				return nil, thumbnail.OpenedSource{}, err
			}
			opened = &source
			openedSource := thumbnail.OpenedSource{
				Identity: thumbnailSourceIdentity(source.Object),
				ETag:     source.Object.ETag,
				Bound:    source.Bound,
			}
			rc := source.Reader
			if !sniffBytes {
				return rc, openedSource, nil
			}
			head := make([]byte, sniffHeadLen)
			n, rerr := io.ReadFull(rc, head)
			if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
				_ = rc.Close()
				return nil, thumbnail.OpenedSource{}, rerr
			}
			head = head[:n]
			replay, aerr := thumbnail.AdmitByMagic(head)
			if aerr != nil {
				_ = rc.Close()
				if errors.Is(aerr, thumbnail.ErrUnsupportedFormat) {
					return nil, thumbnail.OpenedSource{}, fmt.Errorf("%w: unsupported image format %q (supported: image/jpeg, image/png, image/gif)",
						thumbnail.ErrUnsupportedFormat, "webp")
				}
				return nil, thumbnail.OpenedSource{}, fmt.Errorf("%w: %w", service.ErrInvalidArgs, aerr)
			}
			return &sniffedStream{Reader: io.MultiReader(bytes.NewReader(replay), rc), rc: rc}, openedSource, nil
		})
	if err != nil {
		h.writeThumbnailGenerateError(w, r, err)
		return
	}
	if fromCache {
		// Deterministic access evidence: every successful 200 thumbnail
		// response emits exactly one EventAccessed. Misses emit it on stream
		// open inside the service; hits bypass the stream, so the handler
		// emits it here (best-effort like every other event emission). For a
		// pinned request the event names the pinned object.
		h.svc.EmitAccessed(r.Context(), obj)
	}
	// A complete authoritative identity still has a safe opaque validator when
	// cache admission was bypassed. On a miss, however, never fall back to the
	// pre-open validator if the opened source has an incomplete identity.
	etag := statETag
	lastModified := obj.UpdatedAt.UTC().Format(http.TimeFormat)
	versionID := obj.VersionID
	if opened != nil {
		openedIdentity := thumbnailSourceIdentity(opened.Object)
		etag = ""
		lastModified = opened.Object.UpdatedAt.UTC().Format(http.TimeFormat)
		versionID = opened.Object.VersionID
		if opened.Bound && openedIdentity.Complete() {
			etag = thumbValidatorETag(thumbnail.CacheKeyVersion, openedIdentity, opened.Object.ETag, effW, effH)
		} else if !opened.Bound || !openedIdentity.Complete() {
			// The source was read without a complete, reusable identity or
			// through an unbound fallback. Do not let an intermediary retain
			// bytes whose repository generation was not proven, and do not
			// emit a reusable validator or version claim for them.
			cacheControl = "no-store"
			versionID = ""
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setThumbnailETag(w, etag)
	w.Header().Set("Last-Modified", lastModified)
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	w.Header().Set("Cache-Control", cacheControl)
	addThumbnailVary(w)
	if versionID != "" && (version != "" || versioning) {
		// 200 names the served version: opened (miss) or the pre-open Stat
		// (hit — the cache key derives from that Stat's ETag, so obj.VersionID
		// describes the served bytes).
		w.Header().Set("X-Version-Id", versionID)
	}
	// The 200 outcome is the contract (cache hit or miss alike); 304s returned
	// before this point, so exactly one success per 200 response.
	telemetry.IncThumbnailGenerationSuccess(r.Context())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}
