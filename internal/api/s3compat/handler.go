package s3compat

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Handler implements a small subset of the S3 REST API in path-style form.
type Handler struct {
	svc    *service.FileService
	logger *slog.Logger
}

func NewHandler(svc *service.FileService, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger}
}

func (h *Handler) checkBucketPolicy(w http.ResponseWriter, r *http.Request, bucket, action string) bool {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil || cfg.Policy == "" {
		return true
	}
	p, err := auth.ParsePolicy(cfg.Policy)
	if err != nil {
		h.logger.Warn("bucket policy parse error, skipping enforcement", "bucket", bucket, "err", err)
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		host = r.RemoteAddr
	}
	if !auth.Allowed(p, action, host) {
		writeS3Error(w, r, service.ErrForbidden)
		return false
	}
	return true
}

// ── Core Object CRUD ────────────────────────────────────────────────────────

func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	if !h.checkBucketPolicy(w, r, bucket, "s3:PutObject") {
		return
	}
	if src := r.Header.Get("x-amz-copy-source"); src != "" {
		if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
			h.uploadPartCopy(w, r, bucket, key, src, uploadID, partNumberOf(r))
			return
		}
		h.copyObject(w, r, bucket, key, src)
		return
	}
	if r.URL.Query().Has("tagging") {
		h.putObjectTagging(w, r, bucket, key)
		return
	}
	if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
		h.uploadPart(w, r, uploadID, partNumberOf(r))
		return
	}
	if r.URL.Query().Has("acl") {
		h.putObjectACL(w, r, bucket, key)
		return
	}
	meta := s3PutMeta(r.Header)
	if lh := r.Header.Get("x-amz-object-lock-legal-hold"); lh == "ON" || lh == "on" {
		if meta == nil {
			meta = map[string]string{}
		}
		meta["_aero_legal_hold"] = "ON"
	}
	if r.URL.Query().Has("legal-hold") {
		h.putObjectLegalHold(w, r, bucket, key)
		return
	}
	if r.URL.Query().Has("retention") {
		h.putObjectRetention(w, r, bucket, key)
		return
	}
	if r.URL.Query().Has("restore") {
		h.restoreObject(w, r, bucket, key)
		return
	}
	ssecKey := parseSSECKey(r.Header)
	obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), bucket, key, r.Body, r.ContentLength, service.PutOptions{
		ContentType:    r.Header.Get("Content-Type"),
		Metadata:       meta,
		ContentMD5:     r.Header.Get("Content-MD5"),
		StorageClass:   r.Header.Get("x-amz-storage-class"),
		SSECustomerKey: ssecKey,
	})
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if acl := r.Header.Get("x-amz-acl"); acl != "" {
		_ = h.svc.SetObjectACL(r.Context(), mw.TenantFrom(r.Context()), bucket, key, acl)
	}
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) GetObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	if !h.checkBucketPolicy(w, r, bucket, "s3:GetObject") {
		return
	}
	if r.URL.Query().Has("tagging") {
		h.getObjectTagging(w, r, bucket, key)
		return
	}
	if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
		h.listParts(w, r, bucket, key, uploadID)
		return
	}
	if r.URL.Query().Has("acl") {
		h.getObjectACL(w, r, bucket, key)
		return
	}
	if r.URL.Query().Has("legal-hold") {
		h.getObjectLegalHold(w, r, bucket, key)
		return
	}
	if r.URL.Query().Has("retention") {
		h.getObjectRetention(w, r, bucket, key)
		return
	}
	tenant := mw.TenantFrom(r.Context())
	if vid := r.URL.Query().Get("versionId"); vid != "" {
		rc, obj, err := h.svc.GetVersion(r.Context(), tenant, bucket, key, vid)
		if err != nil {
			writeS3Error(w, r, err)
			return
		}
		defer rc.Close()
		writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
		w.Header().Set("x-amz-version-id", obj.VersionID)
		writeS3ObjectMeta(w, obj.Metadata)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rc)
		return
	}
	if hasS3GetConditional(r) {
		if obj, err := h.svc.Stat(r.Context(), tenant, bucket, key); err == nil {
			if status, ok := h.getObjectPreconditions(r, obj); ok {
				writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
				w.WriteHeader(status)
				return
			}
		}
	}
	h.serveObjectContent(w, r, tenant, bucket, key)
}

func (h *Handler) getObjectPreconditions(r *http.Request, obj repository.Object) (int, bool) {
	if !hasS3GetConditional(r) {
		return 0, false
	}
	if code := evalS3GetPreconditions(r, obj); code != 0 {
		return code, true
	}
	return 0, false
}

func (h *Handler) serveObjectContent(w http.ResponseWriter, r *http.Request, tenant, bucket, key string) {
	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		if obj, err := h.svc.Stat(r.Context(), tenant, bucket, key); err == nil {
			off, length, ok, unsat := service.ParseByteRange(rangeHdr, obj.Size)
			if unsat {
				w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(obj.Size, 10))
				writeS3Error(w, r, service.ErrRangeNotSatisfiable)
				return
			}
			if ok {
				rc, _, err := h.svc.GetRange(r.Context(), tenant, bucket, key, off, length)
				if err != nil {
					writeS3Error(w, r, err)
					return
				}
				defer rc.Close()
				writeObjectHeaders(w, obj.ContentType, length, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+length-1, obj.Size))
				writeS3ObjectMeta(w, obj.Metadata)
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.Copy(w, rc)
				return
			}
		}
	}
	rc, obj, err := h.svc.Get(r.Context(), tenant, bucket, key)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer rc.Close()
	writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
	writeS3ObjectMeta(w, obj.Metadata)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (h *Handler) HeadObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	if !h.checkBucketPolicy(w, r, bucket, "s3:GetObject") {
		return
	}
	tenant := mw.TenantFrom(r.Context())
	if vid := r.URL.Query().Get("versionId"); vid != "" {
		rc, obj, err := h.svc.GetVersion(r.Context(), tenant, bucket, key, vid)
		if err != nil {
			writeS3Error(w, r, err)
			return
		}
		_ = rc.Close()
		writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
		w.Header().Set("x-amz-version-id", obj.VersionID)
		writeS3ObjectMeta(w, obj.Metadata)
		w.WriteHeader(http.StatusOK)
		return
	}
	obj, err := h.svc.Stat(r.Context(), tenant, bucket, key)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
	writeS3ObjectMeta(w, obj.Metadata)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) DeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	if !h.checkBucketPolicy(w, r, bucket, "s3:DeleteObject") {
		return
	}
	if r.URL.Query().Has("tagging") {
		h.deleteObjectTagging(w, r, bucket, key)
		return
	}
	if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
		h.abortMultipartUpload(w, r, uploadID)
		return
	}
	if err := h.svc.Delete(r.Context(), mw.TenantFrom(r.Context()), bucket, key, true); err != nil && !errors.Is(err, service.ErrNotFound) {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Bucket Dispatch ─────────────────────────────────────────────────────────

func (h *Handler) BucketDispatch(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	q := r.URL.Query()
	if h.dispatchBucketSubresource(w, r, bucket, q) {
		return
	}
	if !h.checkBucketPolicy(w, r, bucket, "s3:ListBucket") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if q.Has("uploads") {
			h.listMultipartUploads(w, r, bucket)
			return
		}
		if q.Has("cors") {
			h.getBucketCORS(w, r, bucket)
			return
		}
		h.listObjects(w, r, bucket)
	case http.MethodHead:
		h.headBucket(w, r, bucket)
	case http.MethodPut:
		if q.Has("cors") {
			h.putBucketCORS(w, r, bucket)
			return
		}
		if q.Has("delete") {
			h.deleteObjects(w, r, bucket)
			return
		}
		h.createBucket(w, r, bucket)
	case http.MethodPost:
		if q.Has("delete") {
			h.deleteObjects(w, r, bucket)
			return
		}
		if q.Has("cors") {
			h.deleteBucketCORS(w, r, bucket)
			return
		}
		http.Error(w, "unsupported bucket POST", http.StatusBadRequest)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Response helpers ────────────────────────────────────────────────────────

func writeObjectHeaders(w http.ResponseWriter, contentType string, size int64, etag, lastModified, storageClass string, meta map[string]string) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	w.Header().Set("Last-Modified", lastModified)
	w.Header().Set("Accept-Ranges", "bytes")
	if storageClass != "" && storageClass != service.DefaultStorageClass {
		w.Header().Set("x-amz-storage-class", storageClass)
	}
	if v, ok := meta["_aero_content_disposition"]; ok && v != "" {
		w.Header().Set("Content-Disposition", v)
	}
	if v, ok := meta["_aero_content_encoding"]; ok && v != "" {
		w.Header().Set("Content-Encoding", v)
	}
}

func keyFromURL(r *http.Request) string {
	k := chi.URLParam(r, "*")
	return strings.TrimPrefix(k, "/")
}

// parseSSECKey decodes the x-amz-server-side-encryption-customer-key header.
// Returns nil when the header is absent or invalid.
func parseSSECKey(h http.Header) []byte {
	alg := h.Get("x-amz-server-side-encryption-customer-algorithm")
	if alg == "" || strings.ToUpper(alg) != "AES256" {
		return nil
	}
	keyB64 := h.Get("x-amz-server-side-encryption-customer-key")
	if keyB64 == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return nil
	}
	return key
}

func s3PutMeta(h http.Header) map[string]string {
	meta := extractMetaHeaders(h)
	if v := h.Get("Content-Disposition"); v != "" {
		if meta == nil {
			meta = map[string]string{}
		}
		meta["_aero_content_disposition"] = v
	}
	if v := h.Get("Content-Encoding"); v != "" {
		if meta == nil {
			meta = map[string]string{}
		}
		meta["_aero_content_encoding"] = v
	}
	return meta
}

func writeS3ObjectMeta(w http.ResponseWriter, meta map[string]string) {
	for k, v := range meta {
		if strings.HasPrefix(k, "_aero_") {
			continue
		}
		w.Header().Set("x-amz-meta-"+k, v)
	}
	if v, ok := meta["_aero_content_md5"]; ok && v != "" {
		w.Header().Set("x-amz-checksum-md5", v)
	}
}

func extractMetaHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-amz-meta-") {
			out[strings.TrimPrefix(lower, "x-amz-meta-")] = v[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
