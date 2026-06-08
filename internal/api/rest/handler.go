package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Handler binds REST routes to the FileService.
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

func keyFromPath(r *http.Request) string {
	// chi wildcard match comes back as "*" param.
	k := chi.URLParam(r, "*")
	return strings.TrimPrefix(k, "/")
}

// PUT /v1/files/*key — raw upload.
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	// Write preconditions: If-Match (overwrite-if-matches) / If-None-Match:*
	// (create-only) for optimistic concurrency.
	if r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "" {
		cur, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key)
		if !h.checkWritePreconditions(w, r, cur, err == nil) {
			return
		}
	}
	size := r.ContentLength
	ct := r.Header.Get("Content-Type")
	meta := extractMetadataHeaders(r.Header)
	obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, r.Body, size, service.PutOptions{ContentType: ct, Metadata: meta})
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

	obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, file, header.Size, service.PutOptions{ContentType: ct, Metadata: metadata})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toObjectDTO(obj))
}

// GET /v1/files/*key — download. Supports ?version=ID for historical versions.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.allowAnonymous(w, r, key) {
		return
	}
	if v := r.URL.Query().Get("version"); v != "" {
		h.GetSpecificVersion(w, r, key, v)
		return
	}
	tenant := mw.TenantFrom(r.Context())
	// Conditional (304) and Range (206) both need the object's metadata first.
	if hasConditional(r) {
		if obj, err := h.svc.Stat(r.Context(), tenant, service.DefaultBucket, key); err == nil {
			if notModified(r, obj) {
				w.Header().Set("ETag", `"`+obj.ETag+`"`)
				w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
				off, length, ok, unsat := service.ParseByteRange(rangeHdr, obj.Size)
				if unsat {
					w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(obj.Size, 10))
					h.writeError(w, r, service.ErrRangeNotSatisfiable)
					return
				}
				if ok {
					h.serveRange(w, r, tenant, key, obj, off, length)
					return
				}
			}
		}
	}
	rc, obj, err := h.svc.Get(r.Context(), tenant, service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	if obj.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	}
	w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
	writeMetadataHeaders(w, obj.Metadata)
	_, _ = io.Copy(w, rc)
}

// serveRange streams a single byte range as 206 Partial Content.
func (h *Handler) serveRange(w http.ResponseWriter, r *http.Request, tenant, key string, obj repository.Object, off, length int64) {
	rc, _, err := h.svc.GetRange(r.Context(), tenant, service.DefaultBucket, key, off, length)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+length-1, obj.Size))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
	writeMetadataHeaders(w, obj.Metadata)
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.Copy(w, rc)
}

// HEAD /v1/files/*key
func (h *Handler) Head(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.allowAnonymous(w, r, key) {
		return
	}
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
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
	w.WriteHeader(http.StatusOK)
}

// DELETE /v1/files/*key
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	hard := r.URL.Query().Get("hard") == "1"
	if err := h.svc.Delete(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, hard); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/files — list.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	marker := q.Get("marker")
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, err := h.svc.List(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, prefix, marker, limit)
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

// POST /v1/files/*key/presign?op=get|put&expires=<seconds>
func (h *Handler) Presign(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	key = strings.TrimSuffix(key, "/presign")
	op := r.URL.Query().Get("op")
	if op == "" {
		op = "get"
	}
	secs, _ := strconv.Atoi(r.URL.Query().Get("expires"))
	if secs <= 0 {
		secs = 300
	}
	expiry := time.Duration(secs) * time.Second
	var (
		url string
		err error
	)
	switch op {
	case "get":
		url, err = h.svc.PresignGet(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, expiry)
	case "put":
		url, err = h.svc.PresignPut(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, expiry)
	default:
		h.writeError(w, r, fmt.Errorf("%w: op must be get|put", service.ErrInvalidArgs))
		return
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, presignResponse{URL: url, Expires: time.Now().Add(expiry)})
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
	rec, err := h.svc.UploadPart(r.Context(), uploadID, int32(n), r.Body, r.ContentLength)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, partResponse{PartNumber: rec.PartNumber, ETag: rec.ETag, Size: rec.Size})
}

// POST /v1/multipart/{uploadID}/complete
func (h *Handler) CompleteMultipart(w http.ResponseWriter, r *http.Request) {
	uploadID := chi.URLParam(r, "uploadID")
	obj, err := h.svc.CompleteMultipart(r.Context(), uploadID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toObjectDTO(obj))
}

// DELETE /v1/multipart/{uploadID}
func (h *Handler) AbortMultipart(w http.ResponseWriter, r *http.Request) {
	uploadID := chi.URLParam(r, "uploadID")
	if err := h.svc.AbortMultipart(r.Context(), uploadID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	code, message, status := classify(err)
	writeJSON(w, status, errorBody{Error: errorPayload{
		Code: code, Message: message, RequestID: mw.RequestIDFrom(r.Context()),
	}})
}

func classify(err error) (string, string, int) {
	if code, msg, status, ok := classifyLock(err); ok {
		return code, msg, status
	}
	switch {
	case errors.Is(err, service.ErrQuotaExceeded):
		return "QuotaExceeded", err.Error(), http.StatusInsufficientStorage
	case errors.Is(err, service.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		return "NotFound", "object not found", http.StatusNotFound
	case errors.Is(err, service.ErrUploadNotFound), errors.Is(err, repository.ErrUploadNotFound):
		return "NoSuchUpload", "upload not found", http.StatusNotFound
	case errors.Is(err, service.ErrInvalidArgs):
		return "InvalidArgument", err.Error(), http.StatusBadRequest
	case errors.Is(err, service.ErrRangeNotSatisfiable):
		return "InvalidRange", "requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable
	case errors.Is(err, service.ErrPreconditionFailed):
		return "PreconditionFailed", "precondition failed", http.StatusPreconditionFailed
	case errors.Is(err, service.ErrForbidden):
		return "AccessDenied", "access denied", http.StatusForbidden
	default:
		return "InternalError", err.Error(), http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeMetadataHeaders emits stored user metadata as X-Meta-<key> response headers
// (the inverse of extractMetadataHeaders), so GET/HEAD return the metadata a PUT
// stored — previously it was write-only.
func writeMetadataHeaders(w http.ResponseWriter, meta map[string]string) {
	for k, v := range meta {
		w.Header().Set("X-Meta-"+k, v)
	}
}

// extractMetadataHeaders pulls user-metadata from X-Amz-Meta-* and X-Meta-*.
func extractMetadataHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		lower := strings.ToLower(k)
		switch {
		case strings.HasPrefix(lower, "x-amz-meta-"):
			out[strings.TrimPrefix(lower, "x-amz-meta-")] = strings.Join(v, ",")
		case strings.HasPrefix(lower, "x-meta-"):
			out[strings.TrimPrefix(lower, "x-meta-")] = strings.Join(v, ",")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}
