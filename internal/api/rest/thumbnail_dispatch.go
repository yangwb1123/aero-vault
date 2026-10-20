package rest

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Thumbnail preserves the legacy /thumbnail ambiguity contract: an exact
// readable object/version wins; only a full-key miss (or the documented
// key-length artifact) derives the trimmed key. Explicit representations avoid
// this discriminator and call serveFileThumbnail on the full source key.
func (h *Handler) Thumbnail(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	query := r.URL.Query()
	version, pinned := query.Get("version"), query.Has("version")
	var err error
	if pinned {
		// A current Stat cannot discriminate a readable soft-deleted version.
		// Keep this lookup on the original context, outside derivation timeout.
		_, err = h.svc.StatVersionWithOptions(r.Context(), mw.TenantFrom(r.Context()),
			service.DefaultBucket, key, version, service.ReadOptions{})
	} else {
		_, err = h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key)
	}
	if h.serveExactThumbnailKey(w, r, key, pinned, err) {
		return
	}
	h.serveFileThumbnail(w, r, strings.TrimSuffix(key, "/thumbnail"), version)
}

func (h *Handler) serveExactThumbnailKey(w http.ResponseWriter, r *http.Request, key string, pinned bool, err error) bool {
	if errors.Is(err, service.ErrForbidden) && !auth.IsAnonymous(r.Context()) {
		h.writeError(w, r, err)
		return true
	}
	switch {
	case err == nil, errors.Is(err, service.ErrForbidden):
		// Only anonymous pre-capability lookup can recover through full-key
		// canned-public admission. Exact HEAD uses metadata only; derived HEAD
		// retains the decode pipeline. A successful lookup still reauthorizes.
		if r.Method == http.MethodHead {
			h.headFileContent(w, r)
		} else {
			h.Get(w, r)
		}
	case errors.Is(err, service.ErrNotFound):
		return false
	case errors.Is(err, service.ErrInvalidArgs) && (!pinned || len(key) > service.MaxKeyLen):
		// Legacy unpinned behavior falls through on InvalidArgs. A pinned
		// real-but-unreadable source (e.g. SSE-C) must not shadow another key.
		return false
	default:
		// Corrupt, SSE-C, provider failure, etc. never mean a full-key miss.
		h.writeError(w, r, err)
	}
	return true
}

func (h *Handler) serveFileThumbnail(w http.ResponseWriter, r *http.Request, key, version string) {
	// Both legacy trimmed-key and explicit full-key derivation share the gates,
	// deadline and existing identity-bound cache/decode pipeline, in this order.
	if !h.checkBucketPolicy(w, r, key, "s3:GetObject") || !h.allowAnonymous(w, r, key) {
		return
	}
	if h.thumbnailTimeout > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), h.thumbnailTimeout)
		defer cancel()
		r = r.WithContext(ctx)
	}
	h.thumbnailDerive(w, r, key, version)
}
