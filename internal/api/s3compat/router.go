package s3compat

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/service"
)

// NewRouter mounts S3-compatible routes. Operations are dispatched by HTTP
// method on each path; bucket-scoped paths handle ListObjectsV2/HeadBucket/
// CreateBucket, while wildcard key paths handle the object verbs.
func NewRouter(svc *service.FileService, logger *slog.Logger, authz AuthorizationProvider) chi.Router {
	h := NewHandler(svc, logger, authz)
	r := chi.NewRouter()
	r.Use(h.accessLogMiddleware)

	// Bucket-only paths: with or without trailing slash.
	r.HandleFunc("/{bucket}", h.BucketDispatch)
	r.HandleFunc("/{bucket}/", h.BucketDispatch)

	// Object verbs.
	r.Put("/{bucket}/*", h.PutObject)
	r.Get("/{bucket}/*", h.GetObject)
	r.Head("/{bucket}/*", h.HeadObject)
	r.Delete("/{bucket}/*", h.DeleteObject)
	r.Post("/{bucket}/*", h.PostObject) // multipart create/complete
	return r
}
