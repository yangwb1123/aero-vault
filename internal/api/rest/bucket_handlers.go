package rest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// ── Bucket Policy ──────────────────────────────────────────────────────────────

// GET /v1/buckets/{bucket}/policy
func (h *Handler) GetBucketPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	policy, err := h.svc.GetBucketPolicy(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"policy": policy})
}

// PUT /v1/buckets/{bucket}/policy  {"policy":"..."}
func (h *Handler) PutBucketPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var body struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if err := h.svc.SetBucketPolicy(r.Context(), mw.TenantFrom(r.Context()), bucket, body.Policy); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"policy": body.Policy})
}

// DELETE /v1/buckets/{bucket}/policy — clear bucket policy.
func (h *Handler) DeleteBucketPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	// Setting an empty policy clears it.
	if err := h.svc.SetBucketPolicy(r.Context(), mw.TenantFrom(r.Context()), bucket, ""); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Bucket CORS ────────────────────────────────────────────────────────────────

// GET /v1/buckets/{bucket}/cors
func (h *Handler) GetBucketCORS(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	rules, err := h.svc.GetBucketCORS(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := make([]map[string]any, len(rules))
	for i, rule := range rules {
		out[i] = map[string]any{
			"allowed_origins": rule.AllowedOrigins,
			"allowed_methods": rule.AllowedMethods,
			"allowed_headers": rule.AllowedHeaders,
			"expose_headers":  rule.ExposeHeaders,
			"max_age_seconds": rule.MaxAgeSeconds,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /v1/buckets/{bucket}/cors
func (h *Handler) PutBucketCORS(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var rules []repository.CORSRule
	if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if err := h.svc.SetBucketCORS(r.Context(), mw.TenantFrom(r.Context()), bucket, rules); err != nil {
		h.writeError(w, r, err)
		return
	}
	if h.corsProvider != nil {
		h.corsProvider.InvalidateBucket(r.Context(), mw.TenantFrom(r.Context()), bucket)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /v1/buckets/{bucket}/cors
func (h *Handler) DeleteBucketCORS(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	if err := h.svc.DeleteBucketCORS(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		h.writeError(w, r, err)
		return
	}
	if h.corsProvider != nil {
		h.corsProvider.InvalidateBucket(r.Context(), mw.TenantFrom(r.Context()), bucket)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Bucket Encryption ──────────────────────────────────────────────────────────

// GET /v1/buckets/{bucket}/encryption
func (h *Handler) GetBucketEncryption(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sse_algorithm":  cfg.SSEAlgorithm,
		"sse_kms_key_id": cfg.SSEKMSKeyId,
	})
}

// PUT /v1/buckets/{bucket}/encryption
func (h *Handler) PutBucketEncryption(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var body struct {
		Algorithm string `json:"sse_algorithm"`
		KMSKeyID  string `json:"sse_kms_key_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if body.Algorithm != "" && body.Algorithm != "AES256" && body.Algorithm != "aws:kms" {
		h.writeError(w, r, errInvalidJSON(nil))
		return
	}
	if body.Algorithm == "aws:kms" && body.KMSKeyID == "" {
		h.writeError(w, r, errInvalidJSON(nil))
		return
	}
	if err := h.svc.SetBucketEncryption(r.Context(), mw.TenantFrom(r.Context()), bucket, body.Algorithm, body.KMSKeyID); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sse_algorithm":  body.Algorithm,
		"sse_kms_key_id": body.KMSKeyID,
	})
}

// DELETE /v1/buckets/{bucket}/encryption
func (h *Handler) DeleteBucketEncryption(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	if err := h.svc.DeleteBucketEncryption(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Bucket CRUD ────────────────────────────────────────────────────────────────

// GET /v1/buckets — list all buckets for the current tenant.
func (h *Handler) ListBuckets(w http.ResponseWriter, r *http.Request) {
	buckets, err := h.svc.ListBuckets(r.Context(), mw.TenantFrom(r.Context()))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if buckets == nil {
		buckets = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": buckets})
}

// DELETE /v1/buckets/{bucket} — cascading delete a bucket and all its objects.
func (h *Handler) DeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	if err := h.svc.DeleteBucket(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// ── Bucket Logging ─────────────────────────────────────────────────────────────

// GET /v1/buckets/{bucket}/logging
func (h *Handler) GetBucketLogging(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	cfg, err := h.svc.GetBucketLogging(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": cfg.Enabled, "target": cfg.Target, "prefix": cfg.Prefix,
	})
}

// PUT /v1/buckets/{bucket}/logging
func (h *Handler) PutBucketLogging(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var body struct {
		Target string `json:"target"`
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if body.Target == "" {
		if err := h.svc.DeleteBucketLogging(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
			h.writeError(w, r, err)
			return
		}
	} else {
		if err := h.svc.SetBucketLogging(r.Context(), mw.TenantFrom(r.Context()), bucket, body.Target, body.Prefix); err != nil {
			h.writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /v1/buckets/{bucket}/logging
func (h *Handler) DeleteBucketLogging(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	if err := h.svc.DeleteBucketLogging(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Bucket Notifications ───────────────────────────────────────────────────────

// GET /v1/buckets/{bucket}/notification
func (h *Handler) GetBucketNotifications(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	rules, err := h.svc.GetBucketNotifications(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if rules == nil {
		rules = []repository.NotificationRule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

// PUT /v1/buckets/{bucket}/notification
func (h *Handler) PutBucketNotifications(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var body struct {
		Rules []repository.NotificationRule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if err := h.svc.SetBucketNotifications(r.Context(), mw.TenantFrom(r.Context()), bucket, body.Rules); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /v1/buckets/{bucket}/notification
func (h *Handler) DeleteBucketNotifications(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	if err := h.svc.DeleteBucketNotifications(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Bucket Lifecycle & Stats ───────────────────────────────────────────────────

// GET /v1/buckets/{bucket}/lifecycle
func (h *Handler) GetBucketLifecycle(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"expire_after_days":                   cfg.ExpireAfterDays,
		"expire_action":                       cfg.ExpireAction,
		"noncurrent_days":                     cfg.NoncurrentDays,
		"noncurrent_count":                    cfg.NoncurrentCount,
		"transition_rules":                    cfg.TransitionRules,
		"noncurrent_transition_days":          cfg.NoncurrentTransitionDays,
		"noncurrent_transition_storage_class": cfg.NoncurrentTransitionStorageClass,
	})
}

// GET /v1/buckets/{bucket}/stats
func (h *Handler) GetBucketStats(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	stats, err := h.svc.BucketStats(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// ── Bucket Versions ────────────────────────────────────────────────────────────

// GET /v1/buckets/{bucket}/versions
func (h *Handler) ListBucketVersions(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	q := r.URL.Query()
	prefix := q.Get("prefix")
	marker := q.Get("key-marker")
	limit, _ := strconv.Atoi(q.Get("max-keys"))
	page, err := h.svc.List(r.Context(), mw.TenantFrom(r.Context()), bucket, prefix, marker, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	type versionEntry struct {
		Key       string `json:"key"`
		VersionID string `json:"version_id"`
		Size      int64  `json:"size"`
		ETag      string `json:"etag"`
		IsLatest  bool   `json:"is_latest"`
		UpdatedAt string `json:"updated_at"`
	}
	var entries []versionEntry
	for _, obj := range page.Objects {
		entries = append(entries, versionEntry{
			Key: obj.Key, VersionID: obj.VersionID,
			Size: obj.Size, ETag: obj.ETag,
			IsLatest: true, UpdatedAt: obj.UpdatedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"versions": entries, "has_more": page.HasMore,
		"next_key_marker": page.NextMarker,
	})
}
