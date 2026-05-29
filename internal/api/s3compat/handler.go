package s3compat

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Handler implements a small subset of the S3 REST API in path-style form:
//
//	PUT    /{bucket}/{key+}      PutObject
//	GET    /{bucket}/{key+}      GetObject
//	HEAD   /{bucket}/{key+}      HeadObject
//	DELETE /{bucket}/{key+}      DeleteObject
//	GET    /{bucket}/?list-type=2 ListObjectsV2
//	HEAD   /{bucket}/            HeadBucket
//	PUT    /{bucket}/            CreateBucket (no-op-ish; registers bucket)
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

func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	// Sub-resource dispatch by header/query.
	if src := r.Header.Get("x-amz-copy-source"); src != "" {
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
	meta := extractMetaHeaders(r.Header)
	obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), bucket, key, r.Body, r.ContentLength, service.PutOptions{
		ContentType: r.Header.Get("Content-Type"),
		Metadata:    meta,
	})
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	// Canned ACL via x-amz-acl header at write time.
	if acl := r.Header.Get("x-amz-acl"); acl != "" {
		_ = h.svc.SetObjectACL(r.Context(), mw.TenantFrom(r.Context()), bucket, key, acl)
	}
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) GetObject(w http.ResponseWriter, r *http.Request) {
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
	tenant := mw.TenantFrom(r.Context())
	// Range request → 206 Partial Content.
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
				writeObjectHeaders(w, obj.ContentType, length, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+length-1, obj.Size))
				for k, v := range obj.Metadata {
					w.Header().Set("x-amz-meta-"+k, v)
				}
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
	writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat))
	for k, v := range obj.Metadata {
		w.Header().Set("x-amz-meta-"+k, v)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (h *Handler) HeadObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), bucket, key)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat))
	for k, v := range obj.Metadata {
		w.Header().Set("x-amz-meta-"+k, v)
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) DeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
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

func (h *Handler) BucketDispatch(w http.ResponseWriter, r *http.Request) {
	// Bucket-scoped operations: ListObjectsV2 (GET), HeadBucket (HEAD), CreateBucket (PUT).
	bucket := chi.URLParam(r, "bucket")
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Has("uploads") {
			h.listMultipartUploads(w, r, bucket)
			return
		}
		h.listObjects(w, r, bucket)
	case http.MethodHead:
		h.headBucket(w, r, bucket)
	case http.MethodPut:
		h.createBucket(w, r, bucket)
	case http.MethodPost:
		if r.URL.Query().Has("delete") {
			h.deleteObjects(w, r, bucket)
			return
		}
		http.Error(w, "unsupported bucket POST", http.StatusBadRequest)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) listObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	if q.Get("list-type") != "2" {
		writeS3Error(w, r, errors.New("only list-type=2 is supported"))
		return
	}
	prefix := q.Get("prefix")
	token := q.Get("continuation-token")
	maxKeys, _ := strconv.Atoi(q.Get("max-keys"))
	if maxKeys <= 0 {
		maxKeys = 1000
	}
	page, err := h.svc.List(r.Context(), mw.TenantFrom(r.Context()), bucket, prefix, token, maxKeys)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listBucketResult{
		Xmlns:             s3Namespace,
		Name:              bucket,
		Prefix:            prefix,
		KeyCount:          len(page.Objects),
		MaxKeys:           maxKeys,
		IsTruncated:       page.HasMore,
		ContinuationToken: token,
		StartAfter:        q.Get("start-after"),
	}
	if page.HasMore {
		out.NextContinuationToken = page.NextMarker
	}
	for _, o := range page.Objects {
		out.Contents = append(out.Contents, listContent{
			Key:          o.Key,
			LastModified: o.UpdatedAt.UTC(),
			ETag:         `"` + o.ETag + `"`,
			Size:         o.Size,
			StorageClass: "STANDARD",
		})
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(out)
}

func (h *Handler) headBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	exists, err := h.svc.HeadBucket(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) createBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := h.svc.CreateBucket(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	if acl := r.Header.Get("x-amz-acl"); acl != "" {
		_ = h.svc.SetBucketACL(r.Context(), mw.TenantFrom(r.Context()), bucket, acl)
	}
	w.Header().Set("Location", "/"+bucket)
	w.WriteHeader(http.StatusOK)
}

func writeObjectHeaders(w http.ResponseWriter, contentType string, size int64, etag, lastModified string) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	w.Header().Set("Last-Modified", lastModified)
	w.Header().Set("Accept-Ranges", "bytes")
}

func keyFromURL(r *http.Request) string {
	k := chi.URLParam(r, "*")
	return strings.TrimPrefix(k, "/")
}

func extractMetaHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-amz-meta-") {
			out[strings.TrimPrefix(lower, "x-amz-meta-")] = strings.Join(v, ",")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
