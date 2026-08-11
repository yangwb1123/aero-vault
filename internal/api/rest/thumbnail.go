package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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
	// every other object read surface.
	if !h.checkBucketPolicy(w, r, key, "s3:GetObject") {
		return
	}
	if !h.allowAnonymous(w, r, key) {
		return
	}
	tenant := mw.TenantFrom(r.Context())
	obj, err := h.svc.Stat(r.Context(), tenant, service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if !strings.HasPrefix(obj.ContentType, "image/") {
		h.writeError(w, r, fmt.Errorf("%w: object is not an image (content-type %q)", service.ErrInvalidArgs, obj.ContentType))
		return
	}
	maxW, _ := strconv.Atoi(r.URL.Query().Get("w"))
	maxH, _ := strconv.Atoi(r.URL.Query().Get("h"))

	etag := fmt.Sprintf("%s-thumb-%dx%d", obj.ETag, maxW, maxH)
	if etagListMatches(r.Header.Get("If-None-Match"), etag) {
		w.Header().Set("ETag", `"`+etag+`"`)
		w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusNotModified)
		return
	}

	rc, _, err := h.svc.Get(r.Context(), tenant, service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer rc.Close() // releases the pinned stream once a parked wait unblocks
	img, err := thumbnail.GenerateContext(r.Context(), rc, maxW, maxH)
	if err != nil {
		// Client gone or the route deadline fired while waiting for a
		// decode slot: the deferred Close releases the stream; do not
		// classify a canceled request as a 400 client error and do not
		// write to a dead connection. MUST precede the ErrImageTooLarge
		// branch so classification order cannot re-wrap context errors.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, thumbnail.ErrImageTooLarge) {
			h.writeError(w, r, thumbnail.ErrImageTooLarge)
			return
		}
		h.writeError(w, r, fmt.Errorf("%w: %v", service.ErrInvalidArgs, err))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", `"`+etag+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}
