package rest

import (
	"encoding/json"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// POST /v1/files/{key}/restore — restores a soft-deleted object.
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
	key := keyWithoutSuffix(r, "/restore")
	if err := h.svc.RestoreObject(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

// POST /v1/batch/delete — batch delete multiple objects.
func (h *Handler) BatchDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Bucket string   `json:"bucket"`
		Keys   []string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if len(req.Keys) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keys is required"})
		return
	}
	if req.Bucket == "" {
		req.Bucket = service.DefaultBucket
	}
	results := h.svc.BatchDelete(r.Context(), mw.TenantFrom(r.Context()), req.Bucket, req.Keys)
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// POST /v1/batch/tag — batch set tags on multiple objects.
func (h *Handler) BatchTag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Bucket string            `json:"bucket"`
		Keys   []string          `json:"keys"`
		Tags   map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if len(req.Keys) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keys is required"})
		return
	}
	if req.Bucket == "" {
		req.Bucket = service.DefaultBucket
	}
	results := h.svc.BatchSetTags(r.Context(), mw.TenantFrom(r.Context()), req.Bucket, req.Keys, req.Tags)
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
