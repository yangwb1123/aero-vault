package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

func (h *Handler) CreateDepartment(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessAdmin(w, r) {
		return
	}
	var request struct {
		Name     string `json:"name"`
		ParentID string `json:"parent_id,omitempty"`
	}
	if !decodeAccessJSON(w, r, &request) {
		return
	}
	department, err := h.access.CreateDepartment(r.Context(), access.Department{
		TenantID: mw.TenantFrom(r.Context()), Name: request.Name, ParentID: request.ParentID,
	})
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, department)
}

func (h *Handler) ListDepartments(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessAdmin(w, r) {
		return
	}
	departments, err := h.access.ListDepartments(r.Context(), mw.TenantFrom(r.Context()))
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"departments": departments})
}

func (h *Handler) GetDepartment(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessAdmin(w, r) {
		return
	}
	department, err := h.access.GetDepartment(
		r.Context(), mw.TenantFrom(r.Context()), chi.URLParam(r, "id"),
	)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	members, err := h.access.ListDepartmentMembers(r.Context(), department.TenantID, department.ID)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"department": department, "members": members})
}

func (h *Handler) DeleteDepartment(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessAdmin(w, r) {
		return
	}
	err := h.access.DeleteDepartment(r.Context(), mw.TenantFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		writeAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) PutDepartmentMember(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessAdmin(w, r) {
		return
	}
	var request struct {
		Role string `json:"role"`
	}
	if r.ContentLength > 0 && !decodeAccessJSON(w, r, &request) {
		return
	}
	member := access.DepartmentMember{
		TenantID: mw.TenantFrom(r.Context()), DepartmentID: chi.URLParam(r, "id"),
		SubjectID: chi.URLParam(r, "subject"), Role: request.Role,
	}
	if err := h.access.PutDepartmentMember(r.Context(), member); err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, member)
}

func (h *Handler) DeleteDepartmentMember(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessAdmin(w, r) {
		return
	}
	err := h.access.DeleteDepartmentMember(
		r.Context(), mw.TenantFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "subject"),
	)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) PutResourceACL(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Bucket        string               `json:"bucket"`
		Key           string               `json:"key"`
		ResourceKind  access.ResourceKind  `json:"resource_kind"`
		PrincipalType access.PrincipalType `json:"principal_type"`
		PrincipalID   string               `json:"principal_id"`
		Actions       []access.Action      `json:"actions"`
		Effect        access.Effect        `json:"effect"`
		Inherit       bool                 `json:"inherit"`
	}
	if !decodeAccessJSON(w, r, &request) {
		return
	}
	if request.Bucket == "" {
		request.Bucket = service.DefaultBucket
	}
	if len(request.Actions) == 0 {
		writeAccessError(w, fmt.Errorf("%w: actions are required", access.ErrInvalidArgument))
		return
	}
	ownerID := h.resourceOwner(r, request.Bucket, request.Key, request.ResourceKind)
	entries := make([]access.ACLEntry, 0, len(request.Actions))
	for _, action := range request.Actions {
		entry, err := h.access.PutACL(r.Context(), access.ACLEntry{
			TenantID: mw.TenantFrom(r.Context()), Bucket: request.Bucket, Key: request.Key,
			ResourceKind: request.ResourceKind, PrincipalType: request.PrincipalType,
			PrincipalID: request.PrincipalID, Action: action, Effect: request.Effect,
			Inherit: request.Inherit, OwnerID: ownerID,
		})
		if err != nil {
			writeAccessError(w, err)
			return
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"entries": entries})
}

func (h *Handler) ListResourceACL(w http.ResponseWriter, r *http.Request) {
	resource, err := h.accessResourceFromQuery(r)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	entries, err := h.access.ListACL(r.Context(), resource)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource": resource, "entries": entries})
}

func (h *Handler) DeleteResourceACL(w http.ResponseWriter, r *http.Request) {
	err := h.access.DeleteACL(r.Context(), mw.TenantFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		writeAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) CheckResourceAccess(w http.ResponseWriter, r *http.Request) {
	resource, err := h.accessResourceFromQuery(r)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	action := access.Action(r.URL.Query().Get("action"))
	if !access.ValidAction(action) {
		writeAccessError(w, fmt.Errorf("%w: unsupported action", access.ErrInvalidArgument))
		return
	}
	principal, _ := access.PrincipalFrom(r.Context())
	decision, err := h.access.Authorize(r.Context(), principal, action, resource)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decision)
}

func (h *Handler) CreateShare(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Bucket     string `json:"bucket"`
		Key        string `json:"key"`
		Name       string `json:"name"`
		Password   string `json:"password"`
		Preview    bool   `json:"allow_preview"`
		Download   bool   `json:"allow_download"`
		MaxUses    int64  `json:"max_uses"`
		ExpiresAt  string `json:"expires_at"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	if !decodeAccessJSON(w, r, &request) {
		return
	}
	if request.Bucket == "" {
		request.Bucket = service.DefaultBucket
	}
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), request.Bucket, request.Key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	expires, err := shareExpiry(request.ExpiresAt, request.TTLSeconds)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	share, token, err := h.access.CreateShare(r.Context(), access.CreateShareRequest{
		TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, Name: request.Name,
		Password: request.Password, AllowPreview: request.Preview, AllowDownload: request.Download,
		MaxUses: request.MaxUses, ExpiresAt: expires, OwnerID: service.ObjectOwner(obj),
	})
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"share": share, "token": token, "url": h.absoluteURL(r, "/share/"+url.PathEscape(token)),
	})
}

func (h *Handler) ListShares(w http.ResponseWriter, r *http.Request) {
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = service.DefaultBucket
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		writeAccessError(w, fmt.Errorf("%w: key is required", access.ErrInvalidArgument))
		return
	}
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), bucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	shares, err := h.access.ListShares(
		r.Context(), obj.TenantID, bucket, key, service.ObjectOwner(obj),
	)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

func (h *Handler) RevokeShare(w http.ResponseWriter, r *http.Request) {
	err := h.access.RevokeShare(r.Context(), mw.TenantFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		writeAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) PublishAsset(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Bucket       string `json:"bucket"`
		Key          string `json:"key"`
		Slug         string `json:"slug"`
		CacheControl string `json:"cache_control"`
	}
	if !decodeAccessJSON(w, r, &request) {
		return
	}
	if request.Bucket == "" {
		request.Bucket = service.DefaultBucket
	}
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), request.Bucket, request.Key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if !strings.HasPrefix(strings.ToLower(obj.ContentType), "image/") {
		writeAccessError(w, fmt.Errorf("%w: only image objects may be published", access.ErrInvalidArgument))
		return
	}
	asset, err := h.access.PublishAsset(r.Context(), access.PublicAsset{
		TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, Slug: request.Slug,
		CacheControl: request.CacheControl, OwnerID: service.ObjectOwner(obj),
	})
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"asset": asset, "url": h.absoluteURL(r, "/public/assets/"+asset.Slug),
	})
}

func (h *Handler) ListAssets(w http.ResponseWriter, r *http.Request) {
	assets, err := h.access.ListPublicAssets(r.Context(), mw.TenantFrom(r.Context()))
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": assets})
}

func (h *Handler) UnpublishAsset(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	err := h.access.UnpublishAsset(r.Context(), mw.TenantFrom(r.Context()), slug)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) accessResourceFromQuery(r *http.Request) (access.Resource, error) {
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = service.DefaultBucket
	}
	kind := access.ResourceKind(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = access.ResourceObject
	}
	if kind != access.ResourceObject && kind != access.ResourceFolder && kind != access.ResourceBucket {
		return access.Resource{}, fmt.Errorf("%w: invalid resource kind", access.ErrInvalidArgument)
	}
	key := r.URL.Query().Get("key")
	return access.Resource{
		TenantID: mw.TenantFrom(r.Context()), Bucket: bucket, Key: key,
		Kind: kind, OwnerID: h.resourceOwner(r, bucket, key, kind),
	}, nil
}

func (h *Handler) resourceOwner(r *http.Request, bucket, key string, kind access.ResourceKind) string {
	if kind == access.ResourceBucket || key == "" {
		return ""
	}
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), bucket, key)
	if err != nil {
		return ""
	}
	return service.ObjectOwner(obj)
}

func (h *Handler) requireAccessAdmin(w http.ResponseWriter, r *http.Request) bool {
	principal, ok := access.PrincipalFrom(r.Context())
	if !ok || !access.IsAdministrator(principal) {
		writeAccessError(w, access.ErrDenied)
		return false
	}
	return true
}

func decodeAccessJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeAccessError(w, fmt.Errorf("%w: invalid JSON: %v", access.ErrInvalidArgument, err))
		return false
	}
	return true
}

func shareExpiry(raw string, ttlSeconds int64) (time.Time, error) {
	if raw != "" && ttlSeconds > 0 {
		return time.Time{}, fmt.Errorf("%w: choose expires_at or ttl_seconds", access.ErrInvalidArgument)
	}
	if ttlSeconds < 0 {
		return time.Time{}, fmt.Errorf("%w: ttl_seconds must be positive", access.ErrInvalidArgument)
	}
	if ttlSeconds > 0 {
		return time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second), nil
	}
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: expires_at must be RFC3339", access.ErrInvalidArgument)
	}
	return parsed, nil
}

func (h *Handler) absoluteURL(r *http.Request, path string) string {
	if h.publicBaseURL != "" {
		return h.publicBaseURL + path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

func writeAccessError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "InternalError"
	switch {
	case errors.Is(err, access.ErrDenied):
		status, code = http.StatusForbidden, "AccessDenied"
	case errors.Is(err, access.ErrNotFound):
		status, code = http.StatusNotFound, "NotFound"
	case errors.Is(err, access.ErrInvalidArgument), errors.Is(err, access.ErrBadPassword):
		status, code = http.StatusBadRequest, "InvalidArgument"
	case errors.Is(err, access.ErrShareExpired):
		status, code = http.StatusGone, "ShareExpired"
	}
	writeJSON(w, status, errorBody{Error: errorPayload{Code: code, Message: err.Error()}})
}
