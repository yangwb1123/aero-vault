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
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

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

	etag := fmt.Sprintf("%s-thumb-%dx%d", obj.ETag, maxW, maxH)
	// Shared-cache directive: public only when this very request was admitted
	// anonymously — allowAnonymous admits anonymous readers solely for
	// genuinely public-readable objects, so the bytes are fetchable by any
	// external anonymous caller under the current policy. An authenticated
	// request proves nothing about public readability (the caller may hold
	// private access), so its response is private (client-local cache only).
	cacheControl := "private, max-age=86400"
	if auth.IsAnonymous(r.Context()) {
		cacheControl = "public, max-age=86400"
	}
	if etagListMatches(r.Header.Get("If-None-Match"), etag) {
		w.Header().Set("ETag", `"`+etag+`"`)
		w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
		// The 304 must mirror the 200's directive (RFC 9111 §3.2/§3.4): a
		// shared cache revalidating a private thumb would otherwise adopt
		// the 304's (absent) directive and store the previous public body.
		w.Header().Set("Cache-Control", cacheControl)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// The decode slot is acquired BEFORE the object stream opens —
	// GenerateContextWithOpener acquires, then invokes the opener (svc.Get),
	// then decodes — so at most maxConcurrentDecodes object streams are open
	// at once, and a request parked on the semaphore holds no stream at all
	// (waiter holds nothing: no fd, no in-flight storage GET). Open failures
	// surface as *OpenError and keep today's writeError classification
	// verbatim; decode and context errors keep the branch below. The stream
	// lifecycle (close on every path, close-before-release) lives inside the
	// API.
	img, err := thumbnail.GenerateContextWithOpener(r.Context(), maxW, maxH, func() (io.ReadCloser, error) {
		rc, _, err := h.svc.Get(r.Context(), tenant, service.DefaultBucket, key)
		if err != nil {
			return nil, err
		}
		if !sniffBytes {
			return rc, nil // bucket 1: declared decodable type, opener unchanged
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
			return nil, rerr
		}
		head = head[:n] // short objects are valid Sniff input → FormatUnknown → 400
		replay, aerr := thumbnail.AdmitByMagic(head)
		if aerr != nil {
			_ = rc.Close() // stream closed on every rejection path
			if errors.Is(aerr, thumbnail.ErrUnsupportedFormat) {
				return nil, fmt.Errorf("%w: unsupported image format %q (supported: image/jpeg, image/png, image/gif)",
					thumbnail.ErrUnsupportedFormat, "webp")
			}
			return nil, fmt.Errorf("%w: %v", service.ErrInvalidArgs, aerr)
		}
		// Admission by magic is a gate input only: the decode pipeline stays
		// the final validity authority (ErrUnsupported → 400). Close must
		// forward to the storage stream — the API's deferred close runs on
		// this wrapper, and io.NopCloser would leak rc.
		return &sniffedStream{Reader: io.MultiReader(bytes.NewReader(replay), rc), rc: rc}, nil
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
		h.writeError(w, r, fmt.Errorf("%w: %v", service.ErrInvalidArgs, err))
		return
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
