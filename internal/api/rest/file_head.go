package rest

import (
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

func (h *Handler) headFileContent(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.checkBucketPolicy(w, r, key, "s3:GetObject") || !h.allowAnonymous(w, r, key) {
		return
	}
	version := r.URL.Query().Get("version")
	obj, err := h.statPinned(r.Context(), mw.TenantFrom(r.Context()), key, version)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if readPreconditionFailed(r, obj) {
		h.writeError(w, r, service.ErrPreconditionFailed)
		return
	}
	if notModified(r, obj) {
		writeContentNotModified(w, obj, version != "")
		return
	}
	writeContentHeaders(w, obj, obj.Size, version != "")
	w.WriteHeader(http.StatusOK)
}
