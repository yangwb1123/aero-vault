package rest

import (
	"encoding/json"
	"errors"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// LegalHoldRequest is the JSON body for PUT /v1/legal-hold.
type LegalHoldRequest struct {
	Key       string `json:"key"`
	Reason    string `json:"reason"`
	VersionID string `json:"version_id,omitempty"`
}

// LegalHoldResponse is the JSON body returned by GET /v1/legal-hold.
type LegalHoldResponse struct {
	Key        string `json:"key"`
	VersionID  string `json:"version_id,omitempty"`
	HoldReason string `json:"hold_reason"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
}

func (h *Handler) PutLegalHold(w http.ResponseWriter, r *http.Request) {
	tenant := mw.TenantFrom(r.Context())

	var req LegalHoldRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, service.ErrInvalidArgs)
		return
	}
	if req.Key == "" {
		h.writeError(w, r, service.ErrInvalidArgs)
		return
	}

	// If no caller identity is available, use "api".
	caller := r.Header.Get("X-Aero-Caller")
	if caller == "" {
		caller = "api"
	}

	if err := h.svc.PutLegalHold(r.Context(), tenant, service.DefaultBucket, req.Key, req.VersionID, req.Reason, caller); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) GetLegalHold(w http.ResponseWriter, r *http.Request) {
	tenant := mw.TenantFrom(r.Context())
	key := r.URL.Query().Get("key")
	if key == "" {
		h.writeError(w, r, service.ErrInvalidArgs)
		return
	}

	versionID := r.URL.Query().Get("versionId")

	hold, err := h.svc.GetLegalHold(r.Context(), tenant, service.DefaultBucket, key, versionID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			// If there's no dedicated legal hold, try listing all holds for the object.
			holds, listErr := h.svc.ListLegalHolds(r.Context(), tenant, service.DefaultBucket, key)
			if listErr != nil || len(holds) == 0 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"legal hold not found"}`))
				return
			}
			hold = holds[0]
		} else {
			h.writeError(w, r, err)
			return
		}
	}

	resp := LegalHoldResponse{
		Key:        key,
		VersionID:  hold.VersionID,
		HoldReason: hold.HoldReason,
		CreatedBy:  hold.CreatedBy,
		CreatedAt:  hold.CreatedAt,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) RemoveLegalHold(w http.ResponseWriter, r *http.Request) {
	tenant := mw.TenantFrom(r.Context())
	key := r.URL.Query().Get("key")
	if key == "" {
		h.writeError(w, r, service.ErrInvalidArgs)
		return
	}

	versionID := r.URL.Query().Get("versionId")

	if err := h.svc.RemoveLegalHold(r.Context(), tenant, service.DefaultBucket, key, versionID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// ListLegalHolds returns all legal holds for an object.
func (h *Handler) ListLegalHolds(w http.ResponseWriter, r *http.Request) {
	tenant := mw.TenantFrom(r.Context())
	key := r.URL.Query().Get("key")
	if key == "" {
		// Without a key, list all legal holds is not supported at the REST level.
		// Return empty list.
		writeJSON(w, http.StatusOK, []LegalHoldResponse{})
		return
	}

	holds, err := h.svc.ListLegalHolds(r.Context(), tenant, service.DefaultBucket, key)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeJSON(w, http.StatusOK, []LegalHoldResponse{})
			return
		}
		h.writeError(w, r, err)
		return
	}
	resp := make([]LegalHoldResponse, 0, len(holds))
	for _, hold := range holds {
		resp = append(resp, LegalHoldResponse{
			Key:        key,
			VersionID:  hold.VersionID,
			HoldReason: hold.HoldReason,
			CreatedBy:  hold.CreatedBy,
			CreatedAt:  hold.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// Ensure repository types are used (import reference).
var _ = repository.LegalHold{}
