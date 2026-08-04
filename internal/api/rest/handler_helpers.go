package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// ── Error handling ─────────────────────────────────────────────────────────────

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
	case errors.Is(err, service.ErrTenantDisabled):
		return "TenantDisabled", service.ErrTenantDisabled.Error(), http.StatusForbidden
	case errors.Is(err, service.ErrQuotaExceeded):
		return "QuotaExceeded", err.Error(), http.StatusInsufficientStorage
	case errors.Is(err, service.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		return "NotFound", "object not found", http.StatusNotFound
	case errors.Is(err, service.ErrUploadNotFound), errors.Is(err, repository.ErrUploadNotFound):
		return "NoSuchUpload", "upload not found", http.StatusNotFound
	case errors.Is(err, service.ErrInvalidArgs):
		return "InvalidArgument", err.Error(), http.StatusBadRequest
	case errors.Is(err, service.ErrBadDigest):
		return "BadDigest", err.Error(), http.StatusBadRequest
	case errors.Is(err, service.ErrMetadataTooLarge),
		errors.Is(err, service.ErrMetadataKeyTooLong),
		errors.Is(err, service.ErrMetadataValueTooLong):
		return "InvalidArgument", err.Error(), http.StatusBadRequest
	case errors.Is(err, service.ErrSizeMismatch):
		return "SizeMismatch", err.Error(), http.StatusBadRequest
	case errors.Is(err, service.ErrRangeNotSatisfiable):
		return "InvalidRange", "requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable
	case errors.Is(err, service.ErrPreconditionFailed):
		return "PreconditionFailed", "precondition failed", http.StatusPreconditionFailed
	case errors.Is(err, service.ErrForbidden):
		return "AccessDenied", "access denied", http.StatusForbidden
	case errors.Is(err, service.ErrObjectCorrupt):
		return "ObjectCorrupt", "object is marked as corrupt", http.StatusGone
	default:
		return "InternalError", err.Error(), http.StatusInternalServerError
	}
}

// ── Conditional & Range handling ───────────────────────────────────────────────

// handleConditional checks read preconditions, cache validators, and Range
// against the stat'd object. It returns true when the request was fully handled.
func (h *Handler) handleConditional(w http.ResponseWriter, r *http.Request, obj repository.Object) bool {
	if readPreconditionFailed(r, obj) {
		h.writeError(w, r, service.ErrPreconditionFailed)
		return true
	}
	if notModified(r, obj) {
		w.Header().Set("ETag", `"`+obj.ETag+`"`)
		w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
		if obj.ContentType != "" {
			w.Header().Set("Content-Type", obj.ContentType)
		}
		writeMetadataHeaders(w, obj.Metadata)
		writeContentMD5(w, obj.Metadata)
		writeContentResponseHeaders(w, obj.Metadata)
		writeStorageClass(w, obj.StorageClass)
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		off, length, ok, unsat := service.ParseByteRange(rangeHdr, obj.Size)
		if unsat {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(obj.Size, 10))
			h.writeError(w, r, service.ErrRangeNotSatisfiable)
			return true
		}
		if ok {
			h.serveRange(w, r, mw.TenantFrom(r.Context()), keyFromPath(r), obj, off, length)
			return true
		}
	}
	return false
}

func (h *Handler) handleRangeOrFull(w http.ResponseWriter, r *http.Request, rc io.ReadCloser, obj repository.Object) {
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
	writeContentMD5(w, obj.Metadata)
	writeContentResponseHeaders(w, obj.Metadata)
	writeStorageClass(w, obj.StorageClass)
	_, _ = io.Copy(w, rc)
}

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
	writeContentMD5(w, obj.Metadata)
	writeContentResponseHeaders(w, obj.Metadata)
	writeStorageClass(w, obj.StorageClass)
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.Copy(w, rc)
}

// ── JSON & Header helpers ──────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeMetadataHeaders(w http.ResponseWriter, meta map[string]string) {
	for k, v := range meta {
		if strings.HasPrefix(strings.ToLower(k), "_aero_") {
			continue
		}
		w.Header().Set("X-Meta-"+k, v)
	}
}

func writeStorageClass(w http.ResponseWriter, sc string) {
	if sc != "" && sc != service.DefaultStorageClass {
		w.Header().Set("X-Storage-Class", sc)
	}
}

func writeContentMD5(w http.ResponseWriter, meta map[string]string) {
	if v, ok := meta["_aero_content_md5"]; ok && v != "" {
		w.Header().Set("X-Content-MD5", v)
	}
}

func writeContentResponseHeaders(w http.ResponseWriter, meta map[string]string) {
	if v, ok := meta["_aero_content_disposition"]; ok && v != "" {
		w.Header().Set("Content-Disposition", v)
	}
	if v, ok := meta["_aero_content_encoding"]; ok && v != "" {
		w.Header().Set("Content-Encoding", v)
	}
}

func extractMetadataHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		lower := strings.ToLower(k)
		switch {
		case strings.HasPrefix(lower, "x-amz-meta-"):
			out[strings.TrimPrefix(lower, "x-amz-meta-")] = v[0]
		case strings.HasPrefix(lower, "x-meta-"):
			out[strings.TrimPrefix(lower, "x-meta-")] = v[0]
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
