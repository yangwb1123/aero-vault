package s3compat

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Handler implements a small subset of the S3 REST API in path-style form.
type Handler struct {
	svc    *service.FileService
	logger *slog.Logger
	authz  AuthorizationProvider // nil = unset = fail-closed deny on delete
}

func NewHandler(svc *service.FileService, logger *slog.Logger, authz AuthorizationProvider) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger, authz: authz}
}

// ── Core Object CRUD ────────────────────────────────────────────────────────

func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeS3Request(w, r) {
		return
	}
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
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
		h.uploadPart(w, r, bucket, key, uploadID, partNumberOf(r))
		return
	}
	if r.URL.Query().Has("acl") {
		h.putObjectACL(w, r, bucket, key)
		return
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
	if !h.checkS3WritePreconditions(w, r, bucket, key) {
		return
	}
	ssec, err := parseSSECRequest(r.Header, ssecHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer ssec.clear()
	tags, err := parseTaggingHeader(r.Header)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	legalHold, err := legalHoldFromHeader(r.Header)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	opts := service.PutOptions{
		ContentType:        r.Header.Get("Content-Type"),
		ContentDisposition: r.Header.Get("Content-Disposition"),
		ContentEncoding:    r.Header.Get("Content-Encoding"),
		Metadata:           extractMetaHeaders(r.Header),
		Tags:               tags,
		ACL:                r.Header.Get("x-amz-acl"),
		LegalHold:          legalHold,
		ContentMD5:         r.Header.Get("Content-MD5"),
		StorageClass:       r.Header.Get("x-amz-storage-class"),
	}
	applyManagedSSEHeaders(r.Header, &opts)
	ssec.applyPutOptions(&opts)
	obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), bucket, key, r.Body, r.ContentLength, opts)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	h.writeCurrentVersionHeader(w, r, obj)
	writeEncryptionHeaders(w, obj.Metadata)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) GetObject(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeS3Request(w, r) {
		return
	}
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
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
	ssec, err := parseSSECRequest(r.Header, ssecHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer ssec.clear()
	readOpts := ssec.readOptions()
	if vid := r.URL.Query().Get("versionId"); vid != "" {
		h.serveObjectVersion(w, r, tenant, bucket, key, vid, readOpts)
		return
	}
	if hasS3GetConditional(r) {
		if obj, err := h.svc.StatWithOptions(r.Context(), tenant, bucket, key, readOpts); err == nil {
			if status, ok := h.getObjectPreconditions(r, obj); ok {
				writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
				h.writeCurrentVersionHeader(w, r, obj)
				w.WriteHeader(status)
				return
			}
		}
	}
	h.serveObjectContent(w, r, tenant, bucket, key, readOpts)
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

func (h *Handler) serveObjectContent(w http.ResponseWriter, r *http.Request, tenant, bucket, key string, opts service.ReadOptions) {
	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		if obj, err := h.svc.StatWithOptions(r.Context(), tenant, bucket, key, opts); err == nil {
			off, length, ok, unsat := service.ParseByteRange(rangeHdr, obj.Size)
			if unsat {
				w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(obj.Size, 10))
				writeS3Error(w, r, service.ErrRangeNotSatisfiable)
				return
			}
			if ok {
				rc, _, err := h.svc.GetRangeWithOptions(r.Context(), tenant, bucket, key, off, length, opts)
				if err != nil {
					writeS3Error(w, r, err)
					return
				}
				defer rc.Close()
				writeObjectHeaders(w, obj.ContentType, length, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+length-1, obj.Size))
				h.writeCurrentVersionHeader(w, r, obj)
				writeS3ObjectMeta(w, obj.Metadata)
				writeEncryptionHeaders(w, obj.Metadata)
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.Copy(w, rc)
				return
			}
		}
	}
	rc, obj, err := h.svc.GetWithOptions(r.Context(), tenant, bucket, key, opts)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer rc.Close()
	writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
	h.writeCurrentVersionHeader(w, r, obj)
	writeS3ObjectMeta(w, obj.Metadata)
	writeEncryptionHeaders(w, obj.Metadata)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (h *Handler) HeadObject(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeS3Request(w, r) {
		return
	}
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	tenant := mw.TenantFrom(r.Context())
	ssec, err := parseSSECRequest(r.Header, ssecHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer ssec.clear()
	readOpts := ssec.readOptions()
	if vid := r.URL.Query().Get("versionId"); vid != "" {
		obj, err := h.svc.StatVersionWithOptions(r.Context(), tenant, bucket, key, vid, readOpts)
		if err != nil {
			writeS3Error(w, r, err)
			return
		}
		writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
		w.Header().Set("x-amz-version-id", obj.VersionID)
		writeEncryptionHeaders(w, obj.Metadata)
		writeS3ObjectMeta(w, obj.Metadata)
		if status := evalS3GetPreconditions(r, obj); status != 0 {
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	obj, err := h.svc.StatWithOptions(r.Context(), tenant, bucket, key, readOpts)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
	h.writeCurrentVersionHeader(w, r, obj)
	writeS3ObjectMeta(w, obj.Metadata)
	writeEncryptionHeaders(w, obj.Metadata)
	if status := evalS3GetPreconditions(r, obj); status != 0 {
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) DeleteObject(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeS3Request(w, r) {
		return
	}
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	if r.URL.Query().Has("tagging") {
		h.deleteObjectTagging(w, r, bucket, key)
		return
	}
	if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
		h.abortMultipartUpload(w, r, bucket, key, uploadID)
		return
	}
	versionID, deleteMarker, err := h.deleteS3Object(
		r.Context(), mw.TenantFrom(r.Context()), bucket, key, r.URL.Query().Get("versionId"),
	)
	if err != nil && !errors.Is(err, service.ErrNotFound) {
		writeS3Error(w, r, err)
		return
	}
	if versionID != "" {
		w.Header().Set("x-amz-version-id", versionID)
	}
	if deleteMarker {
		w.Header().Set("x-amz-delete-marker", "true")
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Bucket Dispatch ─────────────────────────────────────────────────────────

func (h *Handler) BucketDispatch(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeS3Request(w, r) {
		return
	}
	bucket := chi.URLParam(r, "bucket")
	q := r.URL.Query()
	if h.dispatchBucketSubresource(w, r, bucket, q) {
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
	case http.MethodDelete:
		h.deleteBucket(w, r, bucket)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Response helpers ────────────────────────────────────────────────────────

func writeObjectHeaders(w http.ResponseWriter, contentType string, size int64, etag, lastModified, storageClass string, meta map[string]string) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if size >= 0 {
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

func writeS3ObjectMeta(w http.ResponseWriter, meta map[string]string) {
	for k, v := range meta {
		if strings.HasPrefix(strings.ToLower(k), "_aero_") {
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
