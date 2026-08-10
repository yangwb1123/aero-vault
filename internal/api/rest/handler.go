package rest

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Handler binds REST routes to the FileService.
type Handler struct {
	svc           *service.FileService
	logger        *slog.Logger
	corsProvider  mw.BucketCORSProvider
	putPresigner  *auth.PutPresigner
	access        *access.Manager
	publicBaseURL string
}

func (h *Handler) WithAccessManager(manager *access.Manager, publicBaseURL string) *Handler {
	h.access = manager
	h.publicBaseURL = strings.TrimRight(publicBaseURL, "/")
	return h
}

// WithPutPresigner routes presigned transfers back through REST/FileService.
func (h *Handler) WithPutPresigner(p *auth.PutPresigner) *Handler {
	h.putPresigner = p
	return h
}

func NewHandler(svc *service.FileService, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger}
}

// WithCORSProvider attaches a BucketCORSProvider for cache invalidation on
// CORS rule updates. Returns the handler for fluent wiring.
func (h *Handler) WithCORSProvider(p mw.BucketCORSProvider) *Handler {
	h.corsProvider = p
	return h
}

func keyFromPath(r *http.Request) string {
	k := chi.URLParam(r, "*")
	return strings.TrimPrefix(k, "/")
}

// checkBucketPolicy loads the bucket policy and denies the request when the
// action is not allowed for the concrete object/bucket resource. key == ""
// means a bucket-level action (resource = bucket ARN). Returns true when the
// request may proceed.
func (h *Handler) checkBucketPolicy(w http.ResponseWriter, r *http.Request, key, action string) bool {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket)
	if err != nil {
		h.logger.Warn("bucket policy lookup failed; denying request", "bucket", service.DefaultBucket, "err", err)
		h.writeError(w, r, service.ErrForbidden)
		return false
	}
	if cfg.Policy == "" {
		return true
	}
	p, perr := auth.ParsePolicy(cfg.Policy)
	if perr != nil || p == nil {
		h.logger.Warn("bucket policy parse failed; denying request", "bucket", service.DefaultBucket, "err", perr)
		h.writeError(w, r, service.ErrForbidden)
		return false
	}
	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		host = r.RemoteAddr
	}
	if !auth.AllowedResource(p, action, bucketPolicyResourceARN(key), host) {
		h.writeError(w, r, service.ErrForbidden)
		return false
	}
	return true
}

// bucketPolicyResourceARN builds the concrete S3 resource ARN for a REST
// object key. It mirrors internal/api/s3compat/policy.go s3ResourceARN
// byte-for-byte; the /v1 path always targets service.DefaultBucket.
func bucketPolicyResourceARN(key string) string {
	resource := "arn:aws:s3:::" + service.DefaultBucket
	if key != "" {
		resource += "/" + key
	}
	return resource
}

// ── Core CRUD ──────────────────────────────────────────────────────────────────

// PUT /v1/files/*key — raw upload.
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.checkBucketPolicy(w, r, key, "s3:PutObject") {
		return
	}
	if r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "" {
		cur, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key)
		if !h.checkWritePreconditions(w, r, cur, err == nil) {
			return
		}
	}
	size := r.ContentLength
	ct := r.Header.Get("Content-Type")
	meta := extractMetadataHeaders(r.Header)
	obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, r.Body, size, service.PutOptions{
		ContentType:        ct,
		ContentDisposition: r.Header.Get("Content-Disposition"),
		ContentEncoding:    r.Header.Get("Content-Encoding"),
		Metadata:           meta,
		ContentMD5:         r.Header.Get("Content-MD5"),
		StorageClass:       r.Header.Get("x-amz-storage-class"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toObjectDTO(obj))
}

// POST /v1/files — multipart/form-data upload.
func (h *Handler) PostForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: %v", service.ErrInvalidArgs, err))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		h.writeError(w, r, fmt.Errorf("%w: file field required", service.ErrInvalidArgs))
		return
	}
	defer file.Close()

	key := r.FormValue("key")
	if key == "" {
		key = header.Filename
	}
	if !h.checkBucketPolicy(w, r, key, "s3:PutObject") {
		return
	}
	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = mime.TypeByExtension(strings.ToLower(extOf(header.Filename)))
	}
	var metadata map[string]string
	if raw := r.FormValue("metadata"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			h.writeError(w, r, fmt.Errorf("%w: metadata must be JSON object", service.ErrInvalidArgs))
			return
		}
	}
	obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, file, header.Size, service.PutOptions{
		ContentType:        ct,
		ContentDisposition: r.Header.Get("Content-Disposition"),
		ContentEncoding:    r.Header.Get("Content-Encoding"),
		Metadata:           metadata,
		ContentMD5:         r.Header.Get("Content-MD5"),
		StorageClass:       r.Header.Get("x-amz-storage-class"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toObjectDTO(obj))
}

// GET /v1/files/*key — download. Supports ?version=ID for historical versions.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.checkBucketPolicy(w, r, key, "s3:GetObject") {
		return
	}
	if !h.allowAnonymous(w, r, key) {
		return
	}
	if v := r.URL.Query().Get("version"); v != "" {
		h.GetSpecificVersion(w, r, key, v)
		return
	}
	tenant := mw.TenantFrom(r.Context())
	if hasConditional(r) {
		if obj, err := h.svc.Stat(r.Context(), tenant, service.DefaultBucket, key); err == nil {
			if h.handleConditional(w, r, obj) {
				return
			}
		}
	}
	rc, obj, err := h.svc.Get(r.Context(), tenant, service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer rc.Close()
	h.handleRangeOrFull(w, r, rc, obj)
}

// HEAD /v1/files/*key
func (h *Handler) Head(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.checkBucketPolicy(w, r, key, "s3:GetObject") {
		return
	}
	if !h.allowAnonymous(w, r, key) {
		return
	}
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if readPreconditionFailed(r, obj) {
		h.writeError(w, r, service.ErrPreconditionFailed)
		return
	}
	if notModified(r, obj) {
		w.Header().Set("ETag", `"`+obj.ETag+`"`)
		w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
	writeMetadataHeaders(w, obj.Metadata)
	writeContentMD5(w, obj.Metadata)
	writeContentResponseHeaders(w, obj.Metadata)
	writeStorageClass(w, obj.StorageClass)
	w.WriteHeader(http.StatusOK)
}

// DELETE /v1/files/*key
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.checkBucketPolicy(w, r, key, "s3:DeleteObject") {
		return
	}
	hard := r.URL.Query().Get("hard") == "1"
	if err := h.svc.Delete(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, hard); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/files — list.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	// ListBucket is a bucket-level action: resource = bucket ARN.
	if !h.checkBucketPolicy(w, r, "", "s3:ListBucket") {
		return
	}
	q := r.URL.Query()
	prefix := q.Get("prefix")
	marker := q.Get("marker")
	limit, _ := strconv.Atoi(q.Get("limit"))
	var page repository.ListPage
	var err error
	if q.Get("deleted") == "true" {
		page, err = h.svc.ListDeleted(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, prefix, marker, limit)
	} else {
		page, err = h.svc.List(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, prefix, marker, limit)
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := listResponse{HasMore: page.HasMore, NextMarker: page.NextMarker, Objects: make([]objectDTO, 0, len(page.Objects))}
	for _, o := range page.Objects {
		out.Objects = append(out.Objects, toObjectDTO(o))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /v1/multipart
func (h *Handler) InitMultipart(w http.ResponseWriter, r *http.Request) {
	var req initMultipartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: invalid JSON", service.ErrInvalidArgs))
		return
	}
	u, err := h.svc.InitMultipart(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, req.Key, service.PutOptions{
		ContentType: req.ContentType,
		Metadata:    req.Metadata,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, initMultipartResponse{UploadID: u.ID, Key: u.Key, Bucket: u.Bucket})
}

// PUT /v1/multipart/{uploadID}/parts/{n}
func (h *Handler) UploadPart(w http.ResponseWriter, r *http.Request) {
	uploadID := chi.URLParam(r, "uploadID")
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n <= 0 {
		h.writeError(w, r, fmt.Errorf("%w: part number must be a positive integer", service.ErrInvalidArgs))
		return
	}
	rec, err := h.svc.UploadPartFor(
		r.Context(),
		service.MultipartScope{TenantID: mw.TenantFrom(r.Context())},
		uploadID, int32(n), r.Body, r.ContentLength, service.ReadOptions{},
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, partResponse{PartNumber: rec.PartNumber, ETag: rec.ETag, Size: rec.Size})
}

// POST /v1/multipart/{uploadID}/complete
func (h *Handler) CompleteMultipart(w http.ResponseWriter, r *http.Request) {
	uploadID := chi.URLParam(r, "uploadID")
	obj, err := h.svc.CompleteMultipartFor(
		r.Context(),
		service.MultipartScope{TenantID: mw.TenantFrom(r.Context())},
		uploadID, service.ReadOptions{},
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toObjectDTO(obj))
}

// DELETE /v1/multipart/{uploadID}
func (h *Handler) AbortMultipart(w http.ResponseWriter, r *http.Request) {
	uploadID := chi.URLParam(r, "uploadID")
	if err := h.svc.AbortMultipartFor(
		r.Context(),
		service.MultipartScope{TenantID: mw.TenantFrom(r.Context())},
		uploadID,
	); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
