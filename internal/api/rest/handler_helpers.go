package rest

import (
	"context"
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
	"github.com/aero-vault/aero-vault/internal/telemetry"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// ── Error handling ─────────────────────────────────────────────────────────────

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	code, message, status := classify(err)
	writeJSON(w, status, errorBody{Error: errorPayload{
		Code: code, Message: message, RequestID: mw.RequestIDFrom(r.Context()),
	}})
}

// thumbnailRejectionReason maps a thumbnail derivation error to exactly one
// rejection reason label, or ("", true) when the failure is silent (client
// disconnect — no response is emitted, so no counter). Branch order mirrors
// writeThumbnailGenerateError/classify precedence so the reason always matches
// the classify outcome of the error writeError receives; the checks MUST NOT be
// reordered. The sentinel checks (errors.Is) precede the OpenError/SourceReadError
// class checks (errors.As) because Is/As traverse Unwrap chains — that traversal
// is what surfaces the sniff 415/400 reasons (ErrUnsupportedFormat, wrapped
// ErrInvalidArgs→ErrNotAnImage) through *OpenError.
//
// The catch-all returns invalid_argument: writeThumbnailGenerateError's generic
// wrap (fmt.Errorf("%w: %v", service.ErrInvalidArgs, err)) classifies every
// unknown error as 400 InvalidArgument, so the label matches the wire outcome.
// Known edge: an *OpenError whose inner error is context.Canceled races the
// client-disconnect check (the pipeline fast-fails before the opener observes
// the cancel) and wires as 500 while the reason is silent — a sub-millisecond
// window, pre-existing wire behavior (OpenError is unwrapped first), accepted.
func thumbnailRejectionReason(err error) (reason string, silent bool) {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", false
	}
	if errors.Is(err, context.Canceled) {
		return "", true
	}
	if errors.Is(err, thumbnail.ErrImageTooLarge) {
		return "image_too_large", false
	}
	if errors.Is(err, thumbnail.ErrMetadataTooLarge) {
		return "metadata_too_large", false
	}
	if errors.Is(err, thumbnail.ErrSourceTooLarge) {
		return "source_too_large", false
	}
	if errors.Is(err, thumbnail.ErrUnsupportedFormat) {
		return "unsupported_format", false
	}
	if errors.Is(err, thumbnail.ErrNotAnImage) {
		return "not_an_image", false
	}
	if errors.Is(err, thumbnail.ErrUnsupported) {
		return "unsupported", false
	}
	if errors.Is(err, service.ErrInvalidArgs) {
		return "invalid_argument", false
	}
	var oe *thumbnail.OpenError
	var sre *thumbnail.SourceReadError
	if errors.As(err, &oe) || errors.As(err, &sre) {
		return "source_error", false
	}
	return "invalid_argument", false
}

// writeThumbnailError counts a derivation-phase rejection (unless silent) and
// delegates to writeError for the wire response. It is the counting seam for
// the four gate paths in thumbnailDerive that bypass writeThumbnailGenerateError
// (declared non-image 400, declared unsupported 415, ?w=/?h= dims 400) so every
// derivation rejection is observed at the boundary, not just pipeline errors.
func (h *Handler) writeThumbnailError(w http.ResponseWriter, r *http.Request, err error) {
	if reason, silent := thumbnailRejectionReason(err); !silent {
		telemetry.IncThumbnailGenerationRejection(r.Context(), reason)
	}
	h.writeError(w, r, err)
}

// writeThumbnailGenerateError classifies and writes the error returned by the
// thumbnail generation pipeline (GenerateContextWithOpenerCached). It is the
// load-bearing error branch, extracted verbatim from thumbnail.go (which sits
// at the 500-line gate; any further error-branch additions must land here).
// Ordering is pinned by tests and MUST NOT be reordered: the OpenError unwrap
// first (a Get-path failure classifies via writeError → classify, never as a
// silent return), then the server-side deadline (504), then client disconnect
// (silent), then the thumbnail decode sentinels — each unwrapped RAW so
// classify() sees the exact instance (the generic wrap below stringifies via
// %v, destroying the errors.Is chain) — then the marked source-stream failure,
// then the catch-all.
func (h *Handler) writeThumbnailGenerateError(w http.ResponseWriter, r *http.Request, err error) {
	// Outcome observability: count the rejection class up front. Silent client
	// disconnects (context.Canceled) are not counted — no response is emitted.
	if reason, silent := thumbnailRejectionReason(err); !silent {
		telemetry.IncThumbnailGenerationRejection(r.Context(), reason)
	}
	var oe *thumbnail.OpenError
	if errors.As(err, &oe) {
		h.writeError(w, r, oe.Err)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		h.writeError(w, r, service.ErrTimeout)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, thumbnail.ErrImageTooLarge) {
		h.writeError(w, r, thumbnail.ErrImageTooLarge)
		return
	}
	if errors.Is(err, thumbnail.ErrMetadataTooLarge) {
		// The metadata-budget sentinel must reach classify() raw: the
		// generic wrap below stringifies it via %v, so errors.Is would
		// never match downstream. Same unwrap pattern as ErrImageTooLarge.
		h.writeError(w, r, thumbnail.ErrMetadataTooLarge)
		return
	}
	if errors.Is(err, thumbnail.ErrSourceTooLarge) {
		// The source-budget sentinel must reach classify() raw: the generic
		// wrap below stringifies it via %v, so errors.Is would never match
		// downstream. Same unwrap pattern as ErrImageTooLarge/MetadataTooLarge.
		h.writeError(w, r, thumbnail.ErrSourceTooLarge)
		return
	}
	// Mid-decode source-stream failures (storage I/O, on-read
	// verification) are marked by the thumbnail module: classify the
	// underlying error raw — default → 500 InternalError; an
	// ETagVerifier mismatch wraps service.ErrObjectCorrupt → 410 — never
	// 400 InvalidArgument. MUST precede the catch-all: its %v stringify
	// would destroy the chain (same trap as the ErrMetadataTooLarge
	// branch above).
	var sre *thumbnail.SourceReadError
	if errors.As(err, &sre) {
		h.writeError(w, r, sre.Err)
		return
	}
	h.writeError(w, r, fmt.Errorf("%w: %v", service.ErrInvalidArgs, err))
}

func classify(err error) (string, string, int) {
	if code, msg, status, ok := classifyLock(err); ok {
		return code, msg, status
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, service.ErrTimeout):
		// Server-side request deadline: the client may still be connected, so
		// the abort must be visible (504), never a silent empty response.
		return "Timeout", "request timed out", http.StatusGatewayTimeout
	case errors.Is(err, service.ErrTenantDisabled):
		return "TenantDisabled", service.ErrTenantDisabled.Error(), http.StatusForbidden
	case errors.Is(err, service.ErrEntitlementUnavailable):
		return "EntitlementUnavailable", service.ErrEntitlementUnavailable.Error(), http.StatusServiceUnavailable
	case errors.Is(err, service.ErrQuotaExceeded):
		return "QuotaExceeded", err.Error(), http.StatusInsufficientStorage
	case errors.Is(err, service.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		return "NotFound", "object not found", http.StatusNotFound
	case errors.Is(err, service.ErrUploadNotFound), errors.Is(err, repository.ErrUploadNotFound):
		return "NoSuchUpload", "upload not found", http.StatusNotFound
	case errors.Is(err, thumbnail.ErrImageTooLarge):
		// Defensive ordering (not load-bearing): the thumbnail handler unwraps
		// the raw sentinel before classifying (that unwrap is the load-bearing
		// defense), and errors.Is on the raw sentinel never matches
		// ErrInvalidArgs — so the 413 outcome holds regardless of this case's
		// position. Keeping it ahead of ErrInvalidArgs only matters for a
		// hypothetical wrapped ErrInvalidArgs %w ErrImageTooLarge from a
		// future call site.
		return "ImageTooLarge", err.Error(), http.StatusRequestEntityTooLarge
	case errors.Is(err, thumbnail.ErrUnsupportedFormat):
		// Defensive ordering (not load-bearing), mirroring ErrImageTooLarge:
		// the thumbnail handler wraps only ErrUnsupportedFormat (never
		// ErrInvalidArgs), so errors.Is on the raw sentinel cannot match the
		// ErrInvalidArgs case regardless of this case's position.
		return "UnsupportedMediaType", err.Error(), http.StatusUnsupportedMediaType
	case errors.Is(err, thumbnail.ErrMetadataTooLarge):
		// Required, not defensive: the thumbnail handler unwraps the raw
		// sentinel before classifying (that unwrap is the load-bearing
		// defense), and errors.Is on the raw sentinel never matches
		// ErrInvalidArgs — without this case the raw sentinel would fall
		// through to default → 500 InternalError. The class is 413 per RFC
		// 9110 §15.5.17 (Payload Too Large): the request's declared
		// image metadata exceeds the server's processing budget (8 MiB),
		// before any pixel buffer is allocated.
		return "MetadataTooLarge", err.Error(), http.StatusRequestEntityTooLarge
	case errors.Is(err, thumbnail.ErrSourceTooLarge):
		// Required, not defensive: the thumbnail handler unwraps the raw
		// sentinel before classifying (that unwrap is the load-bearing
		// defense), and errors.Is on the raw sentinel never matches
		// ErrInvalidArgs — without this case the raw sentinel would fall
		// through to default → 500 InternalError. The class is 413 per RFC
		// 9110 §15.5.17 (Content Too Large — the RFC 9110 successor to the
		// RFC 7231 "Payload Too Large" title): the request's source image
		// payload exceeds the server's 128 MiB compressed-input processing
		// budget (MaxSourceBytes) and was cut mid-decode by the read cap.
		return "SourceTooLarge", err.Error(), http.StatusRequestEntityTooLarge
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
	case errors.Is(err, mw.ErrBodyTooLarge):
		return "BodyTooLarge", err.Error(), http.StatusRequestEntityTooLarge
	case errors.Is(err, service.ErrRangeNotSatisfiable):
		return "InvalidRange", "requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable
	case errors.Is(err, service.ErrPreconditionFailed):
		return "PreconditionFailed", "precondition failed", http.StatusPreconditionFailed
	case errors.Is(err, mw.ErrBodyTooLarge):
		return "BodyTooLarge", err.Error(), http.StatusRequestEntityTooLarge
	case errors.Is(err, service.ErrForbidden):
		// Surface the denial reason (e.g. default_deny) in the message —
		// the fail-closed delete contract requires operator-visible reasons.

		return "AccessDenied", err.Error(), http.StatusForbidden
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
	// Content-Length is unconditional (mirrors Head, handler.go:249): net/http
	// auto-emits CL only for a written body (server.go:1327), so a 0-byte
	// object would get "Content-Length: 0" on GET but no CL on HEAD — an
	// RFC 9110 §9.3.2 header-set divergence on the exact-key /?version= arms.
	// The explicit "0" is wire-identical to GET's auto-0.
	w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
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
