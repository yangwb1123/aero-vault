package rest

import (
	"fmt"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// DELETE /v1/admin/files/{tenant}/* — operator-facing file deletion in any
// tenant (metadata + object state). The optional `?hard=1` flag drives the
// existing FileService.Delete hard path (storage blob + metadata + audit +
// outbox facts); without it the object is soft-deleted. The tenant comes from
// the path (the admin surface is cross-tenant by design, operator-equivalence
// model, C3); the key is extracted from the chi catch-all segment. This route
// intentionally bypasses the REST bucket-policy guard (F12): admin is the
// operator trust surface, matching every existing admin route. Placed in its
// own file so admin.go stays under the 500-line gate.
func (h *AdminHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	tenant := chiURLParam(r, "tenant")
	if tenant == "" {
		// Reject explicitly: svc.Delete would normalize "" to the default
		// tenant and silently delete from it (F13, non-fail-closed).
		h.writeError(w, r, fmt.Errorf("%w: tenant is required", service.ErrInvalidArgs))
		return
	}
	key := keyFromPath(r)
	hard := r.URL.Query().Get("hard") == "1"
	if err := h.svc.Delete(r.Context(), tenant, service.DefaultBucket, key, hard); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError renders an error through the shared classify mapping (the
// AdminHandler had no writeError of its own; the REST Handler method is
// *Handler-only, so this 4-line wrapper mirrors its shape).
func (h *AdminHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	code, message, status := classify(err)
	writeJSON(w, status, errorBody{Error: errorPayload{
		Code: code, Message: message, RequestID: mw.RequestIDFrom(r.Context()),
	}})
}
