package rest

import (
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
func (h *Handler) Thumbnail(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(keyFromPath(r), "/thumbnail")
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
		w.WriteHeader(http.StatusNotModified)
		return
	}

	rc, _, err := h.svc.Get(r.Context(), tenant, service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer rc.Close()
	img, err := thumbnail.Generate(rc, maxW, maxH)
	if err != nil {
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
