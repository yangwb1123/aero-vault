package rest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/telemetry"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// thumbFreshnessMaxAge bounds the client-side freshness of the derived
// thumbnail resource (package constant, not config). The thumbnail URL
// carries no version pin, so a PUT that replaces the source object is
// invisible to caches keyed on the URL; bounded freshness (max-age) plus
// must-revalidate (RFC 9111 §5.2.2.2) forces revalidation once the window
// lapses, which engages the If-None-Match/re-Stat machinery below — a
// replaced object is observed within this window instead of up to the
// historical 24 h.
const thumbFreshnessMaxAge = 300

// GET /v1/files/<key>/thumbnail?w=&h=
// Generates a JPEG thumbnail of an image object on demand. The response carries
// a derived ETag (source ETag + dimensions) so repeat requests are cacheable
// (304 via If-None-Match).
//
// Dispatch precedence: object keys ending in "/thumbnail" are legal. When an
// object exists at the exact requested key, raw-download semantics win (FR-1)
// and the subresource interpretation applies only when no object exists at
// the full key (FR-2).
func (h *Handler) Thumbnail(w http.ResponseWriter, r *http.Request) {
	fullKey := keyFromPath(r)
	// A ?version= pin requests a specific historical version of the object
	// at the full key. The pre-check/fallback machinery below must never
	// shadow a version-pinned read with derived content of a different key,
	// so delegate to Get (which resolves ?version= via GetSpecificVersion)
	// unconditionally.
	if r.URL.Query().Has("version") {
		h.Get(w, r)
		return
	}
	_, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, fullKey)
	switch {
	case err == nil:
		// The exact key names a real object: raw download wins with full Get
		// semantics (bucket policy, anonymous gate on the full key,
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
	key := strings.TrimSuffix(fullKey, "/thumbnail")
	// Bucket-policy gate on the derivation path, mirroring Get's ordering
	// (policy → anonymous): a policy denying s3:GetObject on the trimmed key
	// must not be bypassable by requesting its derived thumbnail (which
	// returns near-lossless image bytes of the same object). Fail-closed like
	// every other object read surface. Run before the deadline scope: the
	// gates are cheap and fail-closed either way.
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
	tenant := mw.TenantFrom(r.Context())
	obj, err := h.svc.Stat(r.Context(), tenant, service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// Media-type gate, four-bucket partition of the normalized declared
	// Content-Type (RFC 9110 §8.3.1: case-insensitive, parameters stripped —
	// so "Image/JPEG" and "image/jpeg; charset=utf-8" both normalize to
	// image/jpeg and pass):
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
		h.writeError(w, r, fmt.Errorf("%w: object is not an image (content-type %q)", service.ErrInvalidArgs, obj.ContentType))
		return
	}
	if !sniffBytes {
		switch mediaType {
		case "image/jpeg", "image/png", "image/gif":
		default:
			h.writeError(w, r, fmt.Errorf("%w: unsupported image format %q (supported: image/jpeg, image/png, image/gif)",
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
		h.writeError(w, r, err)
		return
	}
	maxH, err := parseThumbDim(q, "h")
	if err != nil {
		h.writeError(w, r, err)
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
	// Pre-open Stat validator: serves ONLY the If-None-Match / 304 fast path
	// below, which short-circuits before the object stream opens. The 200
	// validator is derived separately from the Get-opened object (see the
	// etag computation before the 200 header emission) so a concurrent PUT
	// between this Stat and the open cannot pair a stale validator with the
	// new bytes.
	statETag := fmt.Sprintf("%s-thumb-%dx%d", obj.ETag, effW, effH)
	// Shared-cache directive: public only when this very request was admitted
	// anonymously — allowAnonymous admits anonymous readers solely for
	// genuinely public-readable objects, so the bytes are fetchable by any
	// external anonymous caller under the current policy. An authenticated
	// request proves nothing about public readability (the caller may hold
	// private access), so its response is private (client-local cache only).
	// No Vary: Authorization is emitted (RFC 9110 §12.5.5 SHOULD) — accepted:
	// the bytes are identical across classes (public is reached only for
	// genuinely public-readable objects), contamination is one-way (private
	// responses are never stored by shared caches), and the bounded window
	// below caps any mislabeled entry's lifetime.
	cacheControl := fmt.Sprintf("private, max-age=%d, must-revalidate", thumbFreshnessMaxAge)
	if auth.IsAnonymous(r.Context()) {
		cacheControl = fmt.Sprintf("public, max-age=%d, must-revalidate", thumbFreshnessMaxAge)
	}
	if etagListMatches(r.Header.Get("If-None-Match"), statETag) {
		// Re-observe the object before certifying Not Modified. The pre-open
		// Stat above and this emission are separate moments: a concurrent PUT
		// between them would otherwise pair a stale validator with 304, and a
		// shared cache holding the OLD derived thumbnail would keep serving it
		// (Cache-Control public|private, max-age=300, must-revalidate) until
		// the bounded window lapses — no subsequent PUT invalidates the
		// derived resource. The re-Stat is a repository read
		// only — no stream, no decode slot — so the fast path stays slot-free
		// and stream-free. The 304's validator and Last-Modified are pinned to
		// THIS observation (RFC 9110 §13.1.2: conditions evaluate against the
		// current validator).
		fresh, err := h.svc.Stat(r.Context(), tenant, service.DefaultBucket, key)
		if err != nil {
			// Deleted between the Stats (ErrNotFound → 404) or corrupt (→ 410):
			// never certify Not Modified for a state we could not observe. Same
			// writeError classification as the pre-check Stat.
			h.writeError(w, r, err)
			return
		}
		freshETag := fmt.Sprintf("%s-thumb-%dx%d", fresh.ETag, effW, effH)
		if etagListMatches(r.Header.Get("If-None-Match"), freshETag) {
			w.Header().Set("ETag", `"`+freshETag+`"`)
			w.Header().Set("Last-Modified", fresh.UpdatedAt.UTC().Format(http.TimeFormat))
			// The 304 must mirror the 200's directive (RFC 9111 §4.3.5: the
			// 304's header fields update the stored response, so a divergent
			// directive would rewrite the stored freshness; the
			// public/private split itself is per §3.2). A shared cache
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
	// no stream at all (waiter holds nothing: no fd, no in-flight storage
	// GET). The server-side cache (THUMBNAIL_CACHE_BYTES; keyed by tenant +
	// source ETag + effective dims) is consulted after full authorization
	// and the 304 fast path; a hit returns the stored JPEG with NO slot, NO
	// opener, NO decode (it also emits the access event below so every 200
	// emits exactly one EventAccessed). Open failures surface as *OpenError
	// and keep today's writeError classification verbatim; decode and
	// context errors keep the branch below. The stream lifecycle (close on
	// every path, close-before-release) lives inside the API.
	// The server-side cache (THUMBNAIL_CACHE_BYTES; keyed by tenant + source
	// ETag + effective dims + key version) is consulted after full
	// authorization and the 304 fast path; a hit returns the stored JPEG
	// with NO slot, NO opener, NO decode (it also emits the access event
	// below so every 200 emits exactly one EventAccessed). Two admission
	// gates bypass the cache by design (pre-launch conditions):
	//   - SSE-C objects (SSECustomerInfo ok): the operator's expectation is
	//     that the server never holds bytes derived from customer-keyed
	//     decryption beyond the request — caching the derived JPEG would
	//     persist them indefinitely in server memory.
	//   - multipart uploads (ETag "<md5>-<n>" contains a dash): the ETag is
	//     not content-derived, so the key could pair stale bytes with a
	//     legitimately changed object; only whole-object content MD5 ETags
	//     admit caching.
	cache := h.thumbnailCache
	if _, _, ssec := service.SSECustomerInfo(obj.Metadata); ssec || strings.Contains(obj.ETag, "-") {
		cache = nil
	}
	// The 200-path validator must describe the bytes actually decoded, so
	// the opened object is captured here and read after the pipeline
	// succeeds. Same capture-then-serve ordering the Get handler uses
	// (handler.go -> handleRangeOrFull): the pre-open Stat validator serves
	// only the 304 fast path, never the 200. The opener also reports the
	// opened object's ETag so the cache's store rule can verify content
	// identity before caching (a PUT between the Stat and the open never
	// caches new-version bytes under an old-version key).
	var opened *repository.Object
	img, fromCache, err := thumbnail.GenerateContextWithOpenerCached(
		r.Context(), cache, tenant, obj.ETag, maxW, maxH,
		func() (io.ReadCloser, string, error) {
			rc, o, err := h.svc.Get(r.Context(), tenant, service.DefaultBucket, key)
			if err != nil {
				return nil, "", err
			}
			opened = &o // before the sniff branch: the wrapper does not change the opened object
			if !sniffBytes {
				return rc, o.ETag, nil // bucket 1: declared decodable type, opener unchanged
			}
			// Bucket 3: decide from bytes. The magic head is read from the SAME
			// stream the pipeline will decode (single open, no second svc.Get
			// round-trip) and replayed byte-exactly in front of the live stream,
			// so the pipeline observes the full object (the same replay
			// mechanism generateLocked uses for its DecodeConfig tee).
			head := make([]byte, sniffHeadLen)
			n, rerr := io.ReadFull(rc, head)
			if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
				_ = rc.Close() // storage read failure: OpenError → classify; never "not an image"
				return nil, "", rerr
			}
			head = head[:n] // short objects are valid Sniff input → FormatUnknown → 400
			replay, aerr := thumbnail.AdmitByMagic(head)
			if aerr != nil {
				_ = rc.Close() // stream closed on every rejection path
				if errors.Is(aerr, thumbnail.ErrUnsupportedFormat) {
					return nil, "", fmt.Errorf("%w: unsupported image format %q (supported: image/jpeg, image/png, image/gif)",
						thumbnail.ErrUnsupportedFormat, "webp")
				}
				return nil, "", fmt.Errorf("%w: %v", service.ErrInvalidArgs, aerr)
			}
			// Admission by magic is a gate input only: the decode pipeline stays
			// the final validity authority (ErrUnsupported → 400). Close must
			// forward to the storage stream — the API's deferred close runs on
			// this wrapper, and io.NopCloser would leak rc.
			return &sniffedStream{Reader: io.MultiReader(bytes.NewReader(replay), rc), rc: rc}, o.ETag, nil
		})
	if err != nil {
		// The OpenError unwrap MUST precede the context-error checks: an
		// opener that failed with a canceled ctx is a Get-path failure and
		// classifies exactly as today (writeError → classify), never as a
		// silent return. Load-bearing ordering, pinned by tests.
		var oe *thumbnail.OpenError
		if errors.As(err, &oe) {
			h.writeError(w, r, oe.Err)
			return
		}
		// Server-side route deadline fired while waiting for a decode slot:
		// the client may still be connected — surface a visible 504 (F2)
		// instead of a silent empty 200. MUST precede the ErrImageTooLarge
		// branch so classification order cannot re-wrap context errors.
		if errors.Is(err, context.DeadlineExceeded) {
			h.writeError(w, r, service.ErrTimeout)
			return
		}
		// Client gone (request context canceled by disconnect): no stream is
		// open here (any opened stream was closed inside the API before the
		// slot was released); do not write to a dead connection and do not
		// classify a canceled request as a 400 client error.
		if errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, thumbnail.ErrImageTooLarge) {
			h.writeError(w, r, thumbnail.ErrImageTooLarge)
			return
		}
		if errors.Is(err, thumbnail.ErrMetadataTooLarge) {
			// The metadata-budget sentinel must reach classify() raw: the
			// generic wrap below stringifies it via %v, so errors.Is would
			// never match downstream. Same unwrap pattern as ErrImageTooLarge.
			h.writeError(w, r, thumbnail.ErrMetadataTooLarge)
			return
		}
		// Mid-decode source-stream failures (storage I/O, on-read
		// verification) are marked by the thumbnail module: classify the
		// underlying error raw — default → 500 InternalError; an
		// ETagVerifier mismatch wraps service.ErrObjectCorrupt → 410 — never
		// 400 InvalidArgument. MUST precede the catch-all: its %v stringify
		// would destroy the chain (same trap as the ErrMetadataTooLarge
		// branch above).
		var sre *thumbnail.SourceReadError
		if errors.As(err, &sre) {
			h.writeError(w, r, sre.Err)
			return
		}
		h.writeError(w, r, fmt.Errorf("%w: %v", service.ErrInvalidArgs, err))
		return
	}
	if fromCache {
		// Deterministic access evidence: every successful 200 thumbnail
		// response emits exactly one EventAccessed. Misses emit it on stream
		// open inside the service; hits bypass the stream, so the handler
		// emits it here (best-effort like every other event emission).
		h.svc.EmitAccessed(r.Context(), obj)
	}
	// 200 validator from the opened object: with no intervening write the
	// opened ETag equals the Stat's and the value is byte-identical to
	// today's; under a concurrent PUT it names the version whose bytes were
	// actually decoded. The fallback (defensive; the opener always runs
	// before a successful GenerateContextWithOpener returns) keeps the
	// Stat-derived validator rather than panicking or emitting an empty ETag.
	etag := statETag
	if opened != nil {
		etag = fmt.Sprintf("%s-thumb-%dx%d", opened.ETag, effW, effH)
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", `"`+etag+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	w.Header().Set("Cache-Control", cacheControl)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}

// sniffHeadLen is the longest magic signature Sniff recognizes: RIFF(4) +
// size(4) + WEBP(4) = 12 bytes. Bucket-3 openers read exactly this many
// bytes from the object stream to classify it.
const sniffHeadLen = 12

// sniffedStream replays the sniffed magic head in front of the live object
// stream (io.MultiReader) and forwards Close to the underlying stream, which
// the API's deferred close runs on this wrapper — io.NopCloser would leak it
// (its Close is a no-op). Mirrors the close-forwarding precedent of
// service.ETagVerifier.
type sniffedStream struct {
	io.Reader
	rc io.Closer
}

func (s *sniffedStream) Close() error { return s.rc.Close() }

// parseThumbDim validates one ?w=/?h= thumbnail dimension parameter. An
// absent parameter yields 0 (default-size semantics per Generate's contract).
// Present values must parse as a non-negative integer; parse errors and
// negatives are client argument errors that the caller maps to 400 before any
// cache validator is emitted or the decode pipeline is entered.
func parseThumbDim(q url.Values, name string) (int, error) {
	if !q.Has(name) {
		return 0, nil
	}
	v := q.Get(name)
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid ?%s value %q (must be a non-negative integer)",
			service.ErrInvalidArgs, name, v)
	}
	return n, nil
}
