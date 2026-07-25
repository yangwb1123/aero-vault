package rest

import (
	"encoding/json"
	"fmt"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// PUT /v1/files/{key}/metadata — replace all metadata on an object.
// Body: {"key": "value", ...} — the complete replacement map.
// An empty object {} clears all metadata.
func (h *Handler) PutMetadata(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	key = trimSuffix(key, "/metadata")
	var meta map[string]string
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: invalid JSON: %v", service.ErrInvalidArgs, err))
		return
	}
	if err := h.svc.PutMetadata(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, meta); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// PATCH /v1/files/{key}/metadata — merge metadata keys.
// Body: {"key": "value", ...} — only the provided keys are updated.
func (h *Handler) PatchMetadata(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	key = trimSuffix(key, "/metadata")
	var meta map[string]string
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: invalid JSON: %v", service.ErrInvalidArgs, err))
		return
	}
	if err := h.svc.PatchMetadata(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, meta); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /v1/files/{key}/metadata — clear all metadata or a specific key.
// When ?key=<metaKey> is set, only that key is removed.
// Without query params, all metadata is cleared.
func (h *Handler) DeleteMetadata(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	key = trimSuffix(key, "/metadata")
	metaKey := r.URL.Query().Get("key")
	if metaKey != "" {
		if err := h.svc.DeleteMetadataKey(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, metaKey); err != nil {
			h.writeError(w, r, err)
			return
		}
	} else {
		if err := h.svc.DeleteMetadata(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key); err != nil {
			h.writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// trimSuffix removes suffix from s if present (equivalent to strings.CutSuffix in Go 1.21+).
func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
