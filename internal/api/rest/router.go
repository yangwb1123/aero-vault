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

// initSpecRegistrations populates the OpenAPI spec builder with all routes.
// This is called on package init so the /openapi.json endpoint is always up to date.
func init() {
	RegisterRoutes([]apiRoute{
		// OIDC browser login (registered at the root router when configured).
		{Method: "GET", Path: "/auth/oidc/login", Summary: "Start OIDC Authorization Code + PKCE login", Tag: "auth", Public: true, Status: 302},
		{
			Method: "GET", Path: "/auth/oidc/callback", Summary: "Complete OIDC login", Tag: "auth", Public: true, Status: 302,
			Query: []apiQueryParameter{
				{Name: "code", Description: "Authorization code"},
				{Name: "state", Description: "Browser login state"},
			},
		},
		{Method: "GET", Path: "/auth/oidc/logout", Summary: "End the OIDC browser session", Tag: "auth", Public: true, Status: 302},

		// Files CRUD
		{Method: "GET", Path: "/v1/files", Summary: "List objects", Tag: "files", Status: 200, Response: `{"objects":[{"key":"file.txt","size":100}],"next_marker":""}`},
		{Method: "GET", Path: "/v1/files/{key}", Summary: "Get object", Tag: "files", Status: 200, Response: `{"key":"file.txt","size":100,"etag":"abc123","content_type":"text/plain"}`},
		{Method: "HEAD", Path: "/v1/files/{key}", Summary: "Head object", Tag: "files", Status: 200},
		{Method: "PUT", Path: "/v1/files/{key}", Summary: "Upload object", Tag: "files", Body: `"file content"`, Status: 201, Response: `{"key":"file.txt","etag":"abc123"}`},
		{Method: "POST", Path: "/v1/files/{key}", Summary: "Upload via form or presign/lock/restore", Tag: "files", Status: 200},
		{Method: "PATCH", Path: "/v1/files/{key}", Summary: "Partial update (metadata)", Tag: "files", Status: 200},
		{Method: "DELETE", Path: "/v1/files/{key}", Summary: "Delete object", Tag: "files", Status: 204},
		{Method: "GET", Path: "/v1/files/{key}/tags", Summary: "Get object tags", Tag: "files", Status: 200, Response: `{"tags":{"key":"value"}}`},
		{Method: "PUT", Path: "/v1/files/{key}/tags", Summary: "Set object tags", Tag: "files", Body: `{"key":"value"}`, Status: 200},
		{Method: "DELETE", Path: "/v1/files/{key}/tags", Summary: "Delete object tags", Tag: "files", Status: 204},
		{Method: "GET", Path: "/v1/files/{key}/versions", Summary: "List object versions", Tag: "files", Status: 200, Response: `{"versions":[{"version_id":"v1","size":100}]}`},
		{Method: "GET", Path: "/v1/files/{key}/acl", Summary: "Get object ACL", Tag: "files", Status: 200},
		{Method: "PUT", Path: "/v1/files/{key}/acl", Summary: "Set object ACL", Tag: "files", Status: 200},
		{Method: "GET", Path: "/v1/files/{key}/metadata", Summary: "Get object metadata", Tag: "files", Status: 200},
		{Method: "PUT", Path: "/v1/files/{key}/metadata", Summary: "Replace object metadata", Tag: "files", Status: 200},
		{Method: "PATCH", Path: "/v1/files/{key}/metadata", Summary: "Partial update metadata", Tag: "files", Status: 200},
		{Method: "DELETE", Path: "/v1/files/{key}/metadata", Summary: "Delete metadata key", Tag: "files", Status: 200},
		{Method: "POST", Path: "/v1/files/{key}/restore", Summary: "Restore soft-deleted object", Tag: "files", Status: 200},
		{Method: "POST", Path: "/v1/files/{key}/lock", Summary: "Set object retention lock", Tag: "files", Status: 200},
		{Method: "GET", Path: "/v1/files/{key}/thumbnail", Summary: "Get thumbnail (JPEG)", Tag: "files", Status: 200},
		{
			Method: "POST", Path: "/v1/files/{key}/presign",
			Summary: "Generate presigned URL", Tag: "files", Status: 200,
			Query: []apiQueryParameter{
				{Name: "op", Description: "Operation: get or put"},
				{Name: "expires", Description: "Lifetime in seconds", Type: "integer"},
			},
			Response: `{"url":"https://…","expires":"2026-05-24T12:34:56Z"}`,
		},

		// Multipart
		{Method: "POST", Path: "/v1/multipart", Summary: "Initiate multipart upload", Tag: "multipart", Body: `{"bucket":"default","key":"large.bin"}`, Status: 201, Response: `{"upload_id":"uuid","key":"large.bin"}`},
		{Method: "PUT", Path: "/v1/multipart/{uploadID}/parts/{n}", Summary: "Upload part", Tag: "multipart", Status: 200},
		{Method: "POST", Path: "/v1/multipart/{uploadID}/complete", Summary: "Complete multipart upload", Tag: "multipart", Status: 200, Response: `{"key":"large.bin","etag":"abc-3"}`},
		{Method: "DELETE", Path: "/v1/multipart/{uploadID}", Summary: "Abort multipart upload", Tag: "multipart", Status: 204},

		// Search / Chat / Agent
		{Method: "POST", Path: "/v1/search", Summary: "Semantic search", Tag: "search", Body: `{"query":"find docs","k":10}`, Status: 200, Response: `{"hits":[{"key":"doc.txt","score":0.95}]}`},
		{Method: "POST", Path: "/v1/chat", Summary: "Chat with RAG", Tag: "chat", Body: `{"query":"summarize","k":5,"mode":"hybrid"}`, Status: 200, Response: `{"answer":"summary text","model":"gpt-4o-mini","citations":[]}`},
		{Method: "POST", Path: "/v1/chat/stream", Summary: "Streaming chat", Tag: "chat", Body: `{"query":"tell me a story","k":5,"mode":"vector"}`, Status: 200},
		{Method: "POST", Path: "/v1/agent", Summary: "Autonomous agent", Tag: "agent", Body: `{"query":"find and summarize"}`, Status: 200, Response: `{"answer":"result","model":"gpt-4o-mini","steps":[]}`},
		{Method: "GET", Path: "/v1/lineage/objects/{id}", Summary: "Object AI lineage", Tag: "search", Status: 200, Response: `{"object_id":1,"entries":[{"query":"find docs","cost_micros":100}]}`},

		// Events
		{Method: "GET", Path: "/v1/events/stream", Summary: "SSE event stream", Tag: "events", Status: 200},

		// Buckets
		{Method: "GET", Path: "/v1/buckets", Summary: "List buckets", Tag: "buckets", Status: 200, Response: `{"buckets":["default"]}`},
		{Method: "GET", Path: "/v1/buckets/{bucket}/config", Summary: "Get bucket config", Tag: "buckets", Status: 200, Response: `{"name":"default","versioning":false}`},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/versioning", Summary: "Set bucket versioning", Tag: "buckets", Body: `{"enabled":true}`, Status: 200},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/object-lock", Summary: "Set object lock config", Tag: "buckets", Status: 200},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/lifecycle", Summary: "Set lifecycle policy", Tag: "buckets", Body: `{"days":365,"action":"soft_delete"}`, Status: 200},
		{Method: "GET", Path: "/v1/buckets/{bucket}/lifecycle", Summary: "Get lifecycle policy", Tag: "buckets", Status: 200, Response: `{"expire_after_days":365}`},
		{Method: "GET", Path: "/v1/buckets/{bucket}/acl", Summary: "Get bucket ACL", Tag: "buckets", Status: 200},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/acl", Summary: "Set bucket ACL", Tag: "buckets", Status: 200},
		{Method: "GET", Path: "/v1/buckets/{bucket}/policy", Summary: "Get bucket policy", Tag: "buckets", Status: 200, Response: `{"policy":"..."}`},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/policy", Summary: "Set bucket policy", Tag: "buckets", Body: `{"policy":"..."}`, Status: 200},
		{Method: "DELETE", Path: "/v1/buckets/{bucket}/policy", Summary: "Delete bucket policy", Tag: "buckets", Status: 200},
		{Method: "GET", Path: "/v1/buckets/{bucket}/cors", Summary: "Get bucket CORS", Tag: "buckets", Status: 200, Response: `[{"allowed_origins":["*"],"allowed_methods":["GET"]}]`},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/cors", Summary: "Set bucket CORS", Tag: "buckets", Body: `[{"allowed_origins":["*"]}]`, Status: 200},
		{Method: "DELETE", Path: "/v1/buckets/{bucket}/cors", Summary: "Delete bucket CORS", Tag: "buckets", Status: 200},
		{Method: "GET", Path: "/v1/buckets/{bucket}/encryption", Summary: "Get bucket encryption", Tag: "buckets", Status: 200, Response: `{"sse_algorithm":"AES256"}`},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/encryption", Summary: "Set bucket encryption", Tag: "buckets", Body: `{"sse_algorithm":"AES256"}`, Status: 200},
		{Method: "DELETE", Path: "/v1/buckets/{bucket}/encryption", Summary: "Delete bucket encryption", Tag: "buckets", Status: 200},
		{Method: "GET", Path: "/v1/buckets/{bucket}/website", Summary: "Get bucket website config", Tag: "buckets", Status: 200},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/website", Summary: "Set bucket website config", Tag: "buckets", Body: `{"index_document":{"suffix":"index.html"},"error_document":{"key":"error.html"}}`, Status: 200},
		{Method: "DELETE", Path: "/v1/buckets/{bucket}/website", Summary: "Delete bucket website config", Tag: "buckets", Status: 204},
		{Method: "GET", Path: "/v1/buckets/{bucket}/logging", Summary: "Get bucket logging", Tag: "buckets", Status: 200, Response: `{"enabled":false}`},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/logging", Summary: "Set bucket logging", Tag: "buckets", Status: 200},
		{Method: "DELETE", Path: "/v1/buckets/{bucket}/logging", Summary: "Delete bucket logging", Tag: "buckets", Status: 200},
		{Method: "GET", Path: "/v1/buckets/{bucket}/notification", Summary: "Get bucket notification rules", Tag: "buckets", Status: 200, Response: `{"rules":[]}`},
		{Method: "PUT", Path: "/v1/buckets/{bucket}/notification", Summary: "Set bucket notification rules", Tag: "buckets", Status: 200},
		{Method: "DELETE", Path: "/v1/buckets/{bucket}/notification", Summary: "Delete bucket notification rules", Tag: "buckets", Status: 200},
		{Method: "DELETE", Path: "/v1/buckets/{bucket}", Summary: "Delete bucket", Tag: "buckets", Status: 204},
		{Method: "GET", Path: "/v1/buckets/{bucket}/stats", Summary: "Get bucket stats", Tag: "buckets", Status: 200, Response: `{"object_count":100,"total_size_bytes":10000}`},
		{
			Method: "GET", Path: "/v1/buckets/{bucket}/versions",
			Summary: "List bucket versions", Tag: "buckets", Status: 200,
			Query: []apiQueryParameter{
				{Name: "prefix", Description: "Only keys with this prefix"},
				{Name: "key-marker", Description: "Resume after this key"},
				{Name: "version-id-marker", Description: "Resume within key-marker"},
				{Name: "max-keys", Description: "Combined versions and delete markers", Type: "integer"},
			},
			Response: `{"versions":[{"key":"file.txt","version_id":"v1","size":100,"etag":"abc123","is_latest":true,"updated_at":"2026-01-01T00:00:00Z"}],"has_more":false}`,
		},

		// Usage
		{Method: "GET", Path: "/v1/usage", Summary: "Get tenant usage", Tag: "admin", Status: 200, Response: `{"used_bytes":1000,"max_bytes":1000000}`},

		// Batch
		{Method: "POST", Path: "/v1/batch/delete", Summary: "Batch delete objects", Tag: "files", Body: `{"keys":["a.txt","b.txt"]}`, Status: 200, Response: `{"results":[{"key":"a.txt","deleted":true}]}`},
		{Method: "POST", Path: "/v1/batch/tag", Summary: "Batch set tags", Tag: "files", Body: `{"keys":["a.txt"],"tags":{"k":"v"}}`, Status: 200, Response: `{"results":[{"key":"a.txt"}]}`},

		// Legal hold
		{Method: "GET", Path: "/v1/legal-hold", Summary: "Get legal hold", Tag: "legal-hold", Status: 200},
		{Method: "PUT", Path: "/v1/legal-hold", Summary: "Place legal hold", Tag: "legal-hold", Body: `{"key":"doc.txt","reason":"litigation"}`, Status: 200},
		{Method: "DELETE", Path: "/v1/legal-hold", Summary: "Remove legal hold", Tag: "legal-hold", Status: 204},

		// Folders
		{Method: "GET", Path: "/v1/folders", Summary: "List folders", Tag: "files", Status: 200, Response: `{"folders":["docs/","images/"]}`},
		{Method: "POST", Path: "/v1/folders/{key}", Summary: "Create folder", Tag: "files", Status: 201},
		{Method: "DELETE", Path: "/v1/folders/{key}", Summary: "Delete folder", Tag: "files", Status: 204},

		// Enterprise access, sharing, publishing, and portable export
		{
			Method: "GET", Path: "/v1/access/acl", Summary: "List resource ACL entries", Tag: "access", Status: 200,
			Query: []apiQueryParameter{
				{Name: "bucket", Description: "Resource bucket"},
				{Name: "key", Description: "Object key or folder prefix"},
				{Name: "kind", Description: "Resource kind: object, folder, or bucket"},
			},
		},
		{Method: "PUT", Path: "/v1/access/acl", Summary: "Grant or deny resource actions", Tag: "access", Body: `{"key":"team/","resource_kind":"folder","principal_type":"department","principal_id":"dept-id","actions":["object:read"],"effect":"allow","inherit":true}`, Status: 201},
		{Method: "DELETE", Path: "/v1/access/acl/{id}", Summary: "Delete a resource ACL entry", Tag: "access", Status: 204},
		{
			Method: "GET", Path: "/v1/access/check", Summary: "Evaluate current caller access", Tag: "access", Status: 200,
			Query: []apiQueryParameter{
				{Name: "bucket", Description: "Resource bucket"},
				{Name: "key", Description: "Object key or folder prefix"},
				{Name: "kind", Description: "Resource kind: object, folder, or bucket"},
				{Name: "action", Description: "Exact access action to evaluate"},
			},
		},
		{Method: "POST", Path: "/v1/shares", Summary: "Create a protected share link", Tag: "sharing", Body: `{"key":"images/photo.jpg","allow_preview":true,"allow_download":true,"ttl_seconds":3600}`, Status: 201},
		{
			Method: "GET", Path: "/v1/shares", Summary: "List links for one object", Tag: "sharing", Status: 200,
			Query: []apiQueryParameter{
				{Name: "bucket", Description: "Object bucket"},
				{Name: "key", Description: "Object key"},
			},
		},
		{Method: "DELETE", Path: "/v1/shares/{id}", Summary: "Revoke a share link", Tag: "sharing", Status: 204},
		{Method: "GET", Path: "/share/{token}", Summary: "Read an object through a share capability", Tag: "sharing", Public: true, Status: 200},
		{Method: "HEAD", Path: "/share/{token}", Summary: "Inspect a shared object", Tag: "sharing", Public: true, Status: 200},
		{Method: "POST", Path: "/v1/assets", Summary: "Publish an image under a stable public slug", Tag: "assets", Body: `{"key":"blog/hero.jpg","slug":"blog/hero.jpg","cache_control":"public, max-age=86400"}`, Status: 201},
		{Method: "GET", Path: "/v1/assets", Summary: "List published images", Tag: "assets", Status: 200},
		{Method: "DELETE", Path: "/v1/assets/{slug}", Summary: "Unpublish an image", Tag: "assets", Status: 204},
		{Method: "GET", Path: "/public/assets/{slug}", Summary: "Serve a published image", Tag: "assets", Public: true, Status: 200},
		{Method: "HEAD", Path: "/public/assets/{slug}", Summary: "Inspect a published image", Tag: "assets", Public: true, Status: 200},
		{
			Method: "GET", Path: "/v1/exports/archive", Summary: "Export authorized objects as portable tar.gz", Tag: "backup", Status: 200,
			Query: []apiQueryParameter{
				{Name: "bucket", Description: "Bucket to export"},
				{Name: "prefix", Description: "Only export keys under this prefix"},
			},
		},

		// Admin
		{Method: "GET", Path: "/v1/admin/config", Summary: "Get server config", Tag: "admin", AdminOnly: true, Status: 200, Response: `{"version":"0.9.0"}`},
		{Method: "GET", Path: "/v1/admin/keys", Summary: "List API keys", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "POST", Path: "/v1/admin/keys", Summary: "Add API key", Tag: "admin", AdminOnly: true, Status: 201},
		{Method: "DELETE", Path: "/v1/admin/keys/{token}", Summary: "Revoke API key", Tag: "admin", AdminOnly: true, Status: 204},
		{Method: "POST", Path: "/v1/admin/jwt", Summary: "Issue JWT token", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "POST", Path: "/v1/admin/tenants", Summary: "Create tenant", Tag: "admin", AdminOnly: true, Status: 201},
		{Method: "GET", Path: "/v1/admin/tenants", Summary: "List tenants", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "DELETE", Path: "/v1/admin/tenants/{tenant}", Summary: "Delete tenant", Tag: "admin", AdminOnly: true, Status: 204},
		{Method: "PUT", Path: "/v1/admin/tenants/{tenant}/status", Summary: "Set tenant status", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "PUT", Path: "/v1/admin/tenants/{tenant}/quota", Summary: "Set tenant quota", Tag: "admin", AdminOnly: true, Body: `{"max_bytes":1000000,"max_objects":1000}`, Status: 200},
		{Method: "PUT", Path: "/v1/admin/tenants/{tenant}/budget", Summary: "Set tenant AI budget", Tag: "admin", AdminOnly: true, Body: `{"daily_budget_usd":5.0}`, Status: 200},
		{Method: "PUT", Path: "/v1/admin/buckets/{bucket}/quota", Summary: "Set bucket quota", Tag: "admin", AdminOnly: true, Body: `{"max_bytes":1073741824}`, Status: 200},
		{Method: "GET", Path: "/v1/admin/jobs", Summary: "List background jobs", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "POST", Path: "/v1/admin/jobs/{id}/retry", Summary: "Retry failed job", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "GET", Path: "/v1/admin/webhook-failures", Summary: "List webhook failures", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "GET", Path: "/v1/admin/audit", Summary: "List audit log", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "POST", Path: "/v1/admin/departments", Summary: "Create a department", Tag: "admin", AdminOnly: true, Body: `{"name":"engineering","parent_id":""}`, Status: 201},
		{Method: "GET", Path: "/v1/admin/departments", Summary: "List departments", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "GET", Path: "/v1/admin/departments/{id}", Summary: "Get department and members", Tag: "admin", AdminOnly: true, Status: 200},
		{Method: "DELETE", Path: "/v1/admin/departments/{id}", Summary: "Delete a department", Tag: "admin", AdminOnly: true, Status: 204},
		{Method: "PUT", Path: "/v1/admin/departments/{id}/members/{subject}", Summary: "Add a department member", Tag: "admin", AdminOnly: true, Body: `{"role":"member"}`, Status: 200},
		{Method: "DELETE", Path: "/v1/admin/departments/{id}/members/{subject}", Summary: "Remove a department member", Tag: "admin", AdminOnly: true, Status: 204},
	})
}

// NewRouter returns a sub-router mounted at /v1.
func NewRouter(svc *service.FileService, repo repository.Repository, search *ai.Search, chat *ai.Chat, agent *ai.Agent, bus *events.Bus, reg *auth.Registry, logger *slog.Logger, idemHashBody bool, aiRL, adminRL *mw.RateLimiter, aiTimeout time.Duration, aiDegraded bool, opts ...func(*Handler)) chi.Router {
	h := NewHandler(svc, logger)
	if reg != nil {
		h.WithPutPresigner(reg.PutPresigner())
	}
	for _, opt := range opts {
		opt(h)
	}
	aih := NewAIHandler(repo, search, chat, agent, logger, aiDegraded)
	sse := NewSSEHandler(bus, repo, logger)
	adm := NewAdminHandler(svc, repo, reg)
	r := chi.NewRouter()
	if reg != nil {
		r.Use(requireRESTScope(reg))
	} else {
		r.Use(mw.Auth)
	}

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
	r.Get("/buckets/{bucket}/encryption", h.GetBucketEncryption)
	r.Put("/buckets/{bucket}/encryption", h.PutBucketEncryption)
	r.Delete("/buckets/{bucket}/encryption", h.DeleteBucketEncryption)
	r.Get("/buckets/{bucket}/website", h.GetBucketWebsite)
	r.Put("/buckets/{bucket}/website", h.PutBucketWebsite)
	r.Delete("/buckets/{bucket}/website", h.DeleteBucketWebsite)
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
	if h.access != nil {
		r.Get("/access/acl", h.ListResourceACL)
		r.Put("/access/acl", h.PutResourceACL)
		r.Delete("/access/acl/{id}", h.DeleteResourceACL)
		r.Get("/access/check", h.CheckResourceAccess)
		r.Post("/shares", h.CreateShare)
		r.Get("/shares", h.ListShares)
		r.Delete("/shares/{id}", h.RevokeShare)
		r.Post("/assets", h.PublishAsset)
		r.Get("/assets", h.ListAssets)
		r.Delete("/assets/*", h.UnpublishAsset)
		r.Get("/exports/archive", h.ExportArchive)
	}

	// Batch operations
	r.Post("/batch/delete", h.BatchDelete)
	r.Post("/batch/tag", h.BatchTag)

	// Legal hold endpoints (compliance — block deletion while active)
	r.Get("/legal-hold", h.GetLegalHold)
	r.Put("/legal-hold", h.PutLegalHold)
	r.Delete("/legal-hold", h.RemoveLegalHold)

	// Folder management
	r.Get("/folders", h.ListFolders)
	r.Post("/folders/*", h.CreateFolder)
	r.Delete("/folders/*", h.DeleteFolder)

	// Admin surfaces have an independent per-tenant limiter so administrative
	// traffic can be constrained without reducing ordinary file throughput.
	r.Group(func(r chi.Router) {
		if adminRL != nil {
			r.Use(adminRL.Middleware())
		}
		r.Put("/admin/tenants/{tenant}/quota", adm.SetQuota)
		r.Put("/admin/buckets/{bucket}/quota", adm.PutBucketQuota)
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
		if h.access != nil {
			r.Post("/admin/departments", h.CreateDepartment)
			r.Get("/admin/departments", h.ListDepartments)
			r.Get("/admin/departments/{id}", h.GetDepartment)
			r.Delete("/admin/departments/{id}", h.DeleteDepartment)
			r.Put("/admin/departments/{id}/members/{subject}", h.PutDepartmentMember)
			r.Delete("/admin/departments/{id}/members/{subject}", h.DeleteDepartmentMember)
		}
	})
	return r
}

func requireRESTScope(reg *auth.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := auth.ScopeWrite
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				scope = auth.ScopeRead
			}
			reg.Require(scope)(next).ServeHTTP(w, r)
		})
	}
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
		if h.rejectAnonymousSubresource(w, r) {
			return
		}
		h.GetTags(w, r)
	case strings.HasSuffix(r.URL.Path, "/versions"):
		if h.rejectAnonymousSubresource(w, r) {
			return
		}
		h.ListVersions(w, r)
	case strings.HasSuffix(r.URL.Path, "/acl"):
		if h.rejectAnonymousSubresource(w, r) {
			return
		}
		h.GetObjectACLHandler(w, r)
	case strings.HasSuffix(r.URL.Path, "/metadata"):
		if h.rejectAnonymousSubresource(w, r) {
			return
		}
		h.GetMetadata(w, r)
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
