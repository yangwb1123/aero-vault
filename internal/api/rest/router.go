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
func NewRouter(svc *service.FileService, repo repository.Repository, search *ai.Search, chat *ai.Chat, agent *ai.Agent, bus *events.Bus, reg *auth.Registry, logger *slog.Logger, idemHashBody bool, aiRL *mw.RateLimiter, aiTimeout time.Duration) chi.Router {
	h := NewHandler(svc, logger)
	aih := NewAIHandler(repo, search, chat, agent, logger)
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
	r.Get("/buckets/{bucket}/config", h.GetBucketConfig)
	r.Put("/buckets/{bucket}/versioning", h.PutBucketVersioning)
	r.Put("/buckets/{bucket}/object-lock", h.PutBucketLock)
	r.Put("/buckets/{bucket}/lifecycle", adm.PutBucketLifecycle)
	r.Get("/buckets/{bucket}/acl", h.GetBucketACL)
	r.Put("/buckets/{bucket}/acl", h.PutBucketACL)

	// Self-serve usage
	r.Get("/usage", adm.Usage)

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
	return r
}

// postKey dispatches POST /v1/files/<key>/{presign|lock} to the right handler.
func (h *Handler) postKey(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/presign"):
		h.Presign(w, r)
	case strings.HasSuffix(r.URL.Path, "/lock"):
		h.LockObject(w, r)
	default:
		h.writeError(w, r, errors.Join(service.ErrInvalidArgs, errInvalidPostKey))
	}
}

// putKey dispatches PUT /v1/files/<key>/{tags|acl} vs raw upload.
func (h *Handler) putKey(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/tags"):
		h.PutTags(w, r)
	case strings.HasSuffix(r.URL.Path, "/acl"):
		h.PutObjectACLHandler(w, r)
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

// deleteKey dispatches DELETE /v1/files/<key>/tags vs hard delete.
func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/tags") {
		h.DeleteTags(w, r)
		return
	}
	h.Delete(w, r)
}
