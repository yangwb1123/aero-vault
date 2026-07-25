package rest

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/events"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

var errInvalidPostKey = errors.New("invalid request: use /files/<key>/presign or POST /files multipart form")

// NewRouter returns a sub-router mounted at /v1. idemHashBody folds a hash of
// the request body into Idempotency-Key fingerprints (IDEMPOTENCY_HASH_BODY).
// aiRL is an optional independent rate limiter applied only to AI endpoints
// (/search, /chat, /chat/stream, /agent, /lineage); nil disables AI-specific limiting.
// aiTimeout, when > 0, is applied as a per-request context deadline on AI endpoints.
func NewRouter(svc *service.FileService, repo repository.Repository, search *ai.Search, chat *ai.Chat, agent *ai.Agent, bus *events.Bus, reg *auth.Registry, logger *slog.Logger, idemHashBody bool, aiRL *mw.RateLimiter, aiTimeout time.Duration, aiDegraded bool, opts ...func(*Handler)) chi.Router {
	h := NewHandler(svc, logger)
	for _, opt := range opts {
		opt(h)
	}
	aih := NewAIHandler(repo, search, chat, agent, logger, aiDegraded)
	sse := NewSSEHandler(bus, repo, logger)
	adm := NewAdminHandler(svc, repo, reg)
	r := chi.NewRouter()
	r.Use(mw.Auth)

	r.Get("/files", h.List)
	r.Get("/files/*", h.getKey)
	r.Head("/files/*", h.Head)

	// Object mutations are Idempotency-Key aware (opt-in via the header): a
	// retried write replays the original response instead of duplicating it.
	r.Group(func(r chi.Router) {
		r.Use(idempotency(repo, logger, idemHashBody))
		r.Post("/files", h.PostForm)
		r.Post("/files/*", h.postKey)
		r.Put("/files/*", h.putKey)
		r.Patch("/files/*", h.patchKey)
		r.Delete("/files/*", h.deleteKey)
	})

	r.Post("/multipart", h.InitMultipart)
	r.Put("/multipart/{uploadID}/parts/{n}", h.UploadPart)
	r.Post("/multipart/{uploadID}/complete", h.CompleteMultipart)
	r.Delete("/multipart/{uploadID}", h.AbortMultipart)

	// AI endpoints get their own independent rate limiter (AI_RATE_LIMIT_*) and
	// an optional per-request context deadline (REQUEST_TIMEOUT_SECONDS).
	r.Group(func(r chi.Router) {
		if aiRL != nil {
			r.Use(aiRL.Middleware())
		}
		r.Use(mw.RequestTimeout(aiTimeout))
		r.Post("/search", aih.Search)
		r.Post("/chat", aih.Chat)
		r.Post("/chat/stream", aih.ChatStream)
		r.Post("/agent", aih.Agent)
		r.Get("/lineage/objects/{id}", aih.Lineage)
	})
	r.Get("/events/stream", sse.Stream)

	// Bucket policies
	r.Get("/buckets", h.ListBuckets)
	r.Get("/buckets/{bucket}/config", h.GetBucketConfig)
	r.Put("/buckets/{bucket}/versioning", h.PutBucketVersioning)
	r.Put("/buckets/{bucket}/object-lock", h.PutBucketLock)
	r.Put("/buckets/{bucket}/lifecycle", adm.PutBucketLifecycle)
	r.Get("/buckets/{bucket}/lifecycle", h.GetBucketLifecycle)
	r.Get("/buckets/{bucket}/acl", h.GetBucketACL)
	r.Put("/buckets/{bucket}/acl", h.PutBucketACL)
	r.Get("/buckets/{bucket}/policy", h.GetBucketPolicy)
	r.Put("/buckets/{bucket}/policy", h.PutBucketPolicy)
	r.Delete("/buckets/{bucket}/policy", h.DeleteBucketPolicy)
	r.Get("/buckets/{bucket}/cors", h.GetBucketCORS)
	r.Put("/buckets/{bucket}/cors", h.PutBucketCORS)
	r.Delete("/buckets/{bucket}/cors", h.DeleteBucketCORS)
	r.Get("/buckets/{bucket}/logging", h.GetBucketLogging)
	r.Put("/buckets/{bucket}/logging", h.PutBucketLogging)
	r.Delete("/buckets/{bucket}/logging", h.DeleteBucketLogging)
	r.Get("/buckets/{bucket}/notification", h.GetBucketNotifications)
	r.Put("/buckets/{bucket}/notification", h.PutBucketNotifications)
	r.Delete("/buckets/{bucket}/notification", h.DeleteBucketNotifications)
	r.Delete("/buckets/{bucket}", h.DeleteBucket)

	// Bucket stats
	r.Get("/buckets/{bucket}/stats", h.GetBucketStats)
	r.Get("/buckets/{bucket}/versions", h.ListBucketVersions)

	// Self-serve usage
	r.Get("/usage", adm.Usage)

	// Batch operations
	r.Post("/batch/delete", h.BatchDelete)
	r.Post("/batch/tag", h.BatchTag)

	// Legal hold endpoints (compliance — block deletion while active)
	// Uses query parameter ?key=<encoded-key> to avoid chi wildcard conflicts.
	r.Get("/legal-hold", h.GetLegalHold)
	r.Put("/legal-hold", h.PutLegalHold)
	r.Delete("/legal-hold", h.RemoveLegalHold)

	// Folder management
	r.Get("/folders", h.ListFolders)
	r.Post("/folders/*", h.CreateFolder)
	r.Delete("/folders/*", h.DeleteFolder)

	// Admin surfaces
	r.Put("/admin/tenants/{tenant}/quota", adm.SetQuota)
	r.Put("/admin/tenants/{tenant}/budget", adm.SetBudget)
	r.Get("/admin/keys", adm.ListKeys)
	r.Post("/admin/keys", adm.AddKey)
	r.Delete("/admin/keys/{token}", adm.RevokeKey)
	r.Post("/admin/jwt", adm.IssueJWT)
	r.Get("/admin/webhook-failures", adm.ListWebhookFailures)
	r.Get("/admin/jobs", adm.ListJobs)
	r.Post("/admin/jobs/{id}/retry", adm.RetryJob)
	r.Post("/admin/tenants", adm.CreateTenant)
	r.Get("/admin/tenants", adm.ListTenants)
	r.Delete("/admin/tenants/{tenant}", adm.DeleteTenant)
	r.Put("/admin/tenants/{tenant}/status", adm.SetTenantStatus)
	r.Get("/admin/audit", adm.ListAudit)
	r.Get("/admin/config", adm.GetConfig)
	return r
}

// postKey dispatches POST /v1/files/<key>/{presign|lock|restore} to the right handler.
func (h *Handler) postKey(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/presign"):
		h.Presign(w, r)
	case strings.HasSuffix(r.URL.Path, "/lock"):
		h.LockObject(w, r)
	case strings.HasSuffix(r.URL.Path, "/restore"):
		h.Restore(w, r)
	default:
		h.writeError(w, r, errors.Join(service.ErrInvalidArgs, errInvalidPostKey))
	}
}

// putKey dispatches PUT /v1/files/<key>/{tags|acl|metadata} vs raw upload.
func (h *Handler) putKey(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/tags"):
		h.PutTags(w, r)
	case strings.HasSuffix(r.URL.Path, "/acl"):
		h.PutObjectACLHandler(w, r)
	case strings.HasSuffix(r.URL.Path, "/metadata"):
		h.PutMetadata(w, r)
	default:
		h.Put(w, r)
	}
}

// getKey dispatches GET /v1/files/<key>/{tags|versions|acl} vs raw download.
func (h *Handler) getKey(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/tags"):
		h.GetTags(w, r)
	case strings.HasSuffix(r.URL.Path, "/versions"):
		h.ListVersions(w, r)
	case strings.HasSuffix(r.URL.Path, "/acl"):
		h.GetObjectACLHandler(w, r)
	case strings.HasSuffix(r.URL.Path, "/thumbnail"):
		h.Thumbnail(w, r)
	default:
		h.Get(w, r)
	}
}

// patchKey dispatches PATCH /v1/files/<key>/{metadata} vs error.
func (h *Handler) patchKey(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/metadata"):
		h.PatchMetadata(w, r)
	default:
		h.writeError(w, r, errors.Join(service.ErrInvalidArgs, errInvalidPostKey))
	}
}

// deleteKey dispatches DELETE /v1/files/<key>/{tags|metadata} vs hard delete.
func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/tags"):
		h.DeleteTags(w, r)
	case strings.HasSuffix(r.URL.Path, "/metadata"):
		h.DeleteMetadata(w, r)
	default:
		h.Delete(w, r)
	}
}
