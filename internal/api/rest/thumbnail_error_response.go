package rest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

const thumbnailInternalErrorMessage = "internal server error"

// writeThumbnailSourceError serializes only an allowlisted thumbnail
// classification. Source errors are provider-controlled and may contain URLs,
// paths, bucket names, or credentials, so neither the raw error nor a wrapper's
// message is a valid REST response. Unknown causes are retained only as a
// non-reversible fingerprint in the server log.
func (h *Handler) writeThumbnailSourceError(w http.ResponseWriter, r *http.Request, err error) {
	code, message, status, safe := thumbnailSourceResponse(err)
	if !safe {
		if h.logger != nil {
			h.logger.Error("thumbnail source failure",
				"classification", code, "status", status,
				"error_fingerprint", thumbnailSourceFingerprint(err),
				"request_id", mw.RequestIDFrom(r.Context()))
		}
	}
	writeJSON(w, status, errorBody{Error: errorPayload{
		Code: code, Message: message,
		RequestID: mw.RequestIDFrom(r.Context()),
	}})
}

// thumbnailSourceResponse preserves known REST outcomes while replacing every
// message that could have come from an arbitrary source error. classify is
// used only for its established code/status precedence; safe messages are
// selected independently below.
func thumbnailSourceResponse(err error) (string, string, int, bool) {
	code, _, status := classify(err)
	if code == "InternalError" && (errors.Is(err, thumbnail.ErrUnsupported) || errors.Is(err, thumbnail.ErrNotAnImage)) {
		code, status = "InvalidArgument", http.StatusBadRequest
	}
	if message, ok := thumbnailSafeMessage(code, err); ok {
		return code, message, status, true
	}
	return "InternalError", thumbnailInternalErrorMessage, http.StatusInternalServerError, false
}

func thumbnailSafeMessage(code string, err error) (string, bool) {
	switch code {
	case "Timeout":
		return "request timed out", true
	case "NotFound":
		return "object not found", true
	case "NoSuchUpload":
		return "upload not found", true
	case "ImageTooLarge":
		return thumbnail.ErrImageTooLarge.Error(), true
	case "UnsupportedMediaType":
		if errors.Is(err, thumbnail.ErrUnsupportedContentEncoding) {
			return thumbnailContentEncodingMessage(err), true
		}
		if err == thumbnail.ErrUnsupportedFormat {
			return err.Error(), true
		}
		return `thumbnail: unsupported image format "webp" (supported: image/jpeg, image/png, image/gif)`, true
	case "MetadataTooLarge":
		return thumbnail.ErrMetadataTooLarge.Error(), true
	case "SourceTooLarge":
		return thumbnail.ErrSourceTooLarge.Error(), true
	case "ObjectCorrupt":
		return "object is marked as corrupt", true
	case "InternalError":
		return thumbnailInternalMessage(err)
	case "InvalidArgument":
		return thumbnailInvalidArgumentMessage(err), true
	case "TenantDisabled":
		return service.ErrTenantDisabled.Error(), true
	case "EntitlementUnavailable":
		return service.ErrEntitlementUnavailable.Error(), true
	case "QuotaExceeded":
		return service.ErrQuotaExceeded.Error(), true
	case "BadDigest":
		return service.ErrBadDigest.Error(), true
	case "SizeMismatch":
		return service.ErrSizeMismatch.Error(), true
	case "BodyTooLarge":
		return mw.ErrBodyTooLarge.Error(), true
	case "InvalidRange":
		return service.ErrRangeNotSatisfiable.Error(), true
	case "PreconditionFailed":
		return service.ErrPreconditionFailed.Error(), true
	case "AccessDenied":
		return service.ErrForbidden.Error(), true
	case "ObjectLocked":
		return service.ErrLocked.Error(), true
	default:
		return "", false
	}
}

func thumbnailInternalMessage(err error) (string, bool) {
	if errors.Is(err, thumbnail.ErrNilSource) {
		return "thumbnail source unavailable", true
	}
	if err == context.Canceled {
		return context.Canceled.Error(), true
	}
	if errors.Is(err, thumbnail.ErrUnsupported) {
		return service.ErrInvalidArgs.Error() + ": " + thumbnail.ErrUnsupported.Error(), true
	}
	if errors.Is(err, thumbnail.ErrNotAnImage) {
		return service.ErrInvalidArgs.Error() + ": " + thumbnail.ErrNotAnImage.Error(), true
	}
	return "", false
}

func thumbnailInvalidArgumentMessage(err error) string {
	switch {
	case errors.Is(err, thumbnail.ErrUnsupported):
		return service.ErrInvalidArgs.Error() + ": " + thumbnail.ErrUnsupported.Error()
	case errors.Is(err, thumbnail.ErrNotAnImage):
		return service.ErrInvalidArgs.Error() + ": " + thumbnail.ErrNotAnImage.Error()
	case errors.Is(err, service.ErrMetadataTooLarge):
		return service.ErrMetadataTooLarge.Error()
	case errors.Is(err, service.ErrMetadataKeyTooLong):
		return service.ErrMetadataKeyTooLong.Error()
	case errors.Is(err, service.ErrMetadataValueTooLong):
		return service.ErrMetadataValueTooLong.Error()
	default:
		return service.ErrInvalidArgs.Error()
	}
}

func thumbnailSourceFingerprint(err error) string {
	digest := sha256.Sum256([]byte(err.Error()))
	return hex.EncodeToString(digest[:])
}
