package rest

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/service"
)

type PublicAccessHandler struct {
	svc    *service.FileService
	access *access.Manager
	logger *slog.Logger
}

func NewPublicAccessHandler(
	svc *service.FileService,
	manager *access.Manager,
	logger *slog.Logger,
) *PublicAccessHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PublicAccessHandler{svc: svc, access: manager, logger: logger}
}

func (h *PublicAccessHandler) Share(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	download := r.URL.Query().Get("download") == "1"
	action := access.ActionPreview
	if download {
		action = access.ActionDownload
	}
	password := r.Header.Get("X-Aero-Share-Password")
	if password == "" {
		password = r.URL.Query().Get("password")
	}
	share, principal, err := h.access.ResolveShare(r.Context(), token, password, action, false)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	var beforeRead func() error
	if r.Method == http.MethodGet {
		beforeRead = func() error {
			_, _, consumeErr := h.access.ResolveShare(r.Context(), token, password, action, true)
			return consumeErr
		}
	}
	ctx := access.WithPrincipal(r.Context(), principal)
	h.serveObject(
		w, r.WithContext(ctx), share.TenantID, share.Bucket, share.Key,
		download, "private, no-store", false, beforeRead,
	)
}

func (h *PublicAccessHandler) Asset(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	asset, principal, err := h.access.ResolvePublicAsset(r.Context(), slug)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	ctx := access.WithPrincipal(r.Context(), principal)
	h.serveObject(
		w, r.WithContext(ctx), asset.TenantID, asset.Bucket, asset.Key,
		false, asset.CacheControl, true, nil,
	)
}

func (h *PublicAccessHandler) serveObject(
	w http.ResponseWriter,
	r *http.Request,
	tenant, bucket, key string,
	download bool,
	cacheControl string,
	imageOnly bool,
	beforeRead func() error,
) {
	obj, err := h.svc.Stat(r.Context(), tenant, bucket, key)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	if imageOnly && !strings.HasPrefix(strings.ToLower(obj.ContentType), "image/") {
		writeAccessError(w, fmt.Errorf("%w: published object is no longer an image", access.ErrInvalidArgument))
		return
	}
	if requestETagMatches(r, obj.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", quoteETag(obj.ETag))
	w.Header().Set("Content-Type", obj.ContentType)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if download {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", objectFilename(key)))
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	reader, length, partial, ok := h.publicReader(w, r, tenant, bucket, key, obj.Size, beforeRead)
	if !ok {
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if partial {
		w.WriteHeader(http.StatusPartialContent)
	}
	defer reader.Close()
	if _, err := io.Copy(w, reader); err != nil {
		h.logger.Warn("public object stream failed", "key", key, "err", err)
	}
}

func (h *PublicAccessHandler) publicReader(
	w http.ResponseWriter,
	r *http.Request,
	tenant, bucket, key string,
	size int64,
	beforeRead func() error,
) (io.ReadCloser, int64, bool, bool) {
	offset, length, ranged, unsatisfiable := service.ParseByteRange(r.Header.Get("Range"), size)
	if unsatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return nil, 0, false, false
	}
	if beforeRead != nil {
		if err := beforeRead(); err != nil {
			writeAccessError(w, err)
			return nil, 0, false, false
		}
	}
	var reader io.ReadCloser
	var err error
	if ranged {
		reader, _, err = h.svc.GetRange(r.Context(), tenant, bucket, key, offset, length)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, size))
	} else {
		reader, _, err = h.svc.Get(r.Context(), tenant, bucket, key)
		length = size
	}
	if err != nil {
		h.writeServiceError(w, err)
		return nil, 0, false, false
	}
	return reader, length, ranged, true
}

func requestETagMatches(r *http.Request, etag string) bool {
	wanted := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if wanted == "" {
		return false
	}
	quoted := quoteETag(etag)
	for _, candidate := range strings.Split(wanted, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == quoted || strings.Trim(candidate, `"`) == strings.Trim(etag, `"`) {
			return true
		}
	}
	return false
}

func quoteETag(etag string) string {
	if strings.HasPrefix(etag, `"`) {
		return etag
	}
	return `"` + etag + `"`
}

func objectFilename(key string) string {
	if index := strings.LastIndexByte(key, '/'); index >= 0 {
		return key[index+1:]
	}
	return key
}

func (h *PublicAccessHandler) writeServiceError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "InternalError"
	switch {
	case errors.Is(err, service.ErrNotFound):
		status, code = http.StatusNotFound, "NotFound"
	case errors.Is(err, service.ErrTenantDisabled):
		status, code = http.StatusForbidden, "TenantDisabled"
	case errors.Is(err, service.ErrForbidden):
		status, code = http.StatusForbidden, "AccessDenied"
	case errors.Is(err, service.ErrObjectCorrupt):
		status, code = http.StatusConflict, "ObjectCorrupt"
	}
	writeJSON(w, status, errorBody{Error: errorPayload{Code: code, Message: err.Error()}})
}
