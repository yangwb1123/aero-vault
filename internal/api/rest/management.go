package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Tags / versions / bucket policies live on a separate file from the hot-path
// CRUD handlers so the file Handler stays focused on Put/Get/Head/Delete.

// keyWithoutSuffix trims a known suffix off the chi wildcard, used for
// /v1/files/<key>/tags-style routes.
func keyWithoutSuffix(r *http.Request, suffix string) string {
	k := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	return strings.TrimSuffix(k, suffix)
}

// GET /v1/files/*/tags
func (h *Handler) GetTags(w http.ResponseWriter, r *http.Request) {
	key := keyWithoutSuffix(r, "/tags")
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": obj.Tags})
}

// PUT /v1/files/*/tags  body: {"k":"v",...}
func (h *Handler) PutTags(w http.ResponseWriter, r *http.Request) {
	key := keyWithoutSuffix(r, "/tags")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		h.writeError(w, r, fmt.Errorf("%w: %v", service.ErrInvalidArgs, err))
		return
	}
	tags := map[string]string{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &tags); err != nil {
			h.writeError(w, r, fmt.Errorf("%w: tags must be JSON object", service.ErrInvalidArgs))
			return
		}
	}
	if err := h.svc.SetTags(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, tags); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// DELETE /v1/files/*/tags  — equivalent to PUT with empty body.
func (h *Handler) DeleteTags(w http.ResponseWriter, r *http.Request) {
	key := keyWithoutSuffix(r, "/tags")
	if err := h.svc.SetTags(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, map[string]string{}); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/files/*/versions
func (h *Handler) ListVersions(w http.ResponseWriter, r *http.Request) {
	key := keyWithoutSuffix(r, "/versions")
	versions, err := h.svc.ListVersions(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := make([]versionDTO, 0, len(versions))
	for _, v := range versions {
		out = append(out, toVersionDTO(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": out})
}

// GET /v1/files/*?version=ID
func (h *Handler) GetSpecificVersion(w http.ResponseWriter, r *http.Request, key, version string) {
	rc, obj, err := h.svc.GetVersion(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, version)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer rc.Close()
	// Honour conditional GETs on versioned downloads too: a cache-aware client
	// must be able to receive 304 Not Modified (matches the non-versioned Get).
	if notModified(r, obj) {
		w.Header().Set("ETag", `"`+obj.ETag+`"`)
		w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
		w.Header().Set("X-Version-Id", obj.VersionID)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	w.Header().Set("X-Version-Id", obj.VersionID)
	w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	writeMetadataHeaders(w, obj.Metadata)
	_, _ = io.Copy(w, rc)
}

// POST /v1/files/*/lock  body: {"seconds": 3600}  — apply per-object retention.
func (h *Handler) LockObject(w http.ResponseWriter, r *http.Request) {
	key := keyWithoutSuffix(r, "/lock")
	var req struct {
		Seconds int `json:"seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: invalid JSON", service.ErrInvalidArgs))
		return
	}
	if req.Seconds <= 0 {
		h.writeError(w, r, fmt.Errorf("%w: seconds must be > 0", service.ErrInvalidArgs))
		return
	}
	until := time.Now().Add(time.Duration(req.Seconds) * time.Second)
	if err := h.svc.LockObject(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, until); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"locked_until": until.UTC().Format(time.RFC3339)})
}

// GET /v1/buckets/{bucket}/config
func (h *Handler) GetBucketConfig(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bucketConfigDTO{
		Name: cfg.Name, Versioning: cfg.Versioning, ObjectLockSeconds: cfg.ObjectLockSeconds,
		ExpireAfterDays: cfg.ExpireAfterDays, ExpireAction: cfg.ExpireAction,
	})
}

// PUT /v1/buckets/{bucket}/versioning  body: {"enabled": true}
func (h *Handler) PutBucketVersioning(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: invalid JSON", service.ErrInvalidArgs))
		return
	}
	if err := h.svc.SetBucketVersioning(r.Context(), mw.TenantFrom(r.Context()), bucket, req.Enabled); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versioning": req.Enabled})
}

// PUT /v1/buckets/{bucket}/object-lock  body: {"seconds": 3600}
func (h *Handler) PutBucketLock(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var req struct {
		Seconds int `json:"seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: invalid JSON", service.ErrInvalidArgs))
		return
	}
	if req.Seconds < 0 {
		h.writeError(w, r, fmt.Errorf("%w: seconds must be >= 0", service.ErrInvalidArgs))
		return
	}
	if err := h.svc.SetBucketObjectLock(r.Context(), mw.TenantFrom(r.Context()), bucket, req.Seconds); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object_lock_seconds": req.Seconds})
}

type versionDTO struct {
	VersionID   string     `json:"version_id"`
	Size        int64      `json:"size"`
	ETag        string     `json:"etag"`
	ContentType string     `json:"content_type,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
	LockedUntil *time.Time `json:"locked_until,omitempty"`
}

type bucketConfigDTO struct {
	Name              string `json:"name"`
	Versioning        bool   `json:"versioning"`
	ObjectLockSeconds int    `json:"object_lock_seconds"`
	ExpireAfterDays   int    `json:"expire_after_days,omitempty"`
	ExpireAction      string `json:"expire_action,omitempty"`
}

func toVersionDTO(o repository.Object) versionDTO {
	return versionDTO{
		VersionID: o.VersionID, Size: o.Size, ETag: o.ETag,
		ContentType: o.ContentType, UpdatedAt: o.UpdatedAt,
		DeletedAt: o.DeletedAt, LockedUntil: o.LockedUntil,
	}
}

// classifyLock extends the error classifier so retention-lock errors map to 409.
func classifyLock(err error) (string, string, int, bool) {
	if errors.Is(err, service.ErrLocked) {
		return "ObjectLocked", err.Error(), http.StatusConflict, true
	}
	return "", "", 0, false
}

// statusToString gives the classifier a way to log without importing strconv elsewhere.
func statusToString(c int) string { return strconv.Itoa(c) }
