package rest

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// DELETE /v1/admin/files/{tenant}/* — administrative file deletion (metadata
// plus object state). The optional `?hard=1` flag drives the existing
// FileService.Delete hard path (storage blob + metadata + audit + outbox
// facts); without it the object is soft-deleted. A tenant-scoped admin key is
// confined to its own path tenant; tenant="*" is the cross-tenant operator
// form. This route intentionally bypasses the REST bucket-policy guard (F12)
// because it is an administrative trust surface. Placed in its own file so
// admin.go stays under the 500-line gate.
func (h *AdminHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	tenant := chiURLParam(r, "tenant")
	h.deleteFile(w, r, tenant, service.DefaultBucket, "")
}

// DeleteFileInBucket is the explicit bucket form used by the privileged
// vault.file.delete surface. DeleteFile remains as a compatibility route for
// the original default-bucket admin CLI endpoint.
func (h *AdminHandler) DeleteFileInBucket(w http.ResponseWriter, r *http.Request) {
	bucket := chiURLParam(r, "bucket")
	key := keyFromPath(r)
	legacyKey := strings.TrimPrefix(bucket+"/"+key, "/")
	h.deleteFile(w, r, chiURLParam(r, "tenant"), bucket, legacyKey)
}

func (h *AdminHandler) deleteFile(w http.ResponseWriter, r *http.Request, tenant, bucket, legacyKey string) {
	if !h.requireAdminForTenant(w, r, tenant) {
		return
	}
	if tenant == "" {
		// Reject explicitly: svc.Delete would normalize "" to the default
		// tenant and silently delete from it (F13, non-fail-closed).
		h.writeError(w, r, fmt.Errorf("%w: tenant is required", service.ErrInvalidArgs))
		return
	}
	key := keyFromPath(r)
	if !h.authorizeFileDelete(w, r, tenant, bucket, key) {
		return
	}
	hard := r.URL.Query().Get("hard") == "1"
	ctx := service.WithDeletePermission(r.Context(), access.PermissionVaultFileDelete)
	err := h.svc.AdminDelete(ctx, tenant, bucket, key, hard)
	if errors.Is(err, service.ErrNotFound) && legacyKey != "" {
		// The compatibility fallback is a different authorization resource.
		// Re-check it before attempting the delete so a key-aware provider
		// cannot be bypassed by a missing explicit-bucket object.
		if !h.authorizeFileDelete(w, r, tenant, service.DefaultBucket, legacyKey) {
			return
		}
		err = h.svc.AdminDelete(ctx, tenant, service.DefaultBucket, legacyKey, hard)
	}
	if err != nil {
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
