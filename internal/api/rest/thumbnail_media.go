package rest

import (
	"context"
	"fmt"
	"mime"
	"strings"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// thumbnailMediaType keeps preflight and opened-source classification identical.
// ParseMediaType normalizes case/parameters; only actual parse failures and the
// generic binary declaration are byte-decided. Bare tokens remain non-images.
func thumbnailMediaType(contentType string) (sniff bool, err error) {
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil || mediaType == "application/octet-stream" {
		return true, nil
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return false, fmt.Errorf("%w: object is not an image (content-type %q)", service.ErrInvalidArgs, contentType)
	}
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif":
		return false, nil
	default:
		return false, fmt.Errorf("%w: unsupported image format %q (supported: image/jpeg, image/png, image/gif)",
			thumbnail.ErrUnsupportedFormat, contentType)
	}
}

// The opened object, not the earlier Stat, decides whether to sniff. Encoding
// and opener failures retain precedence. Rejections own cleanup; accepted
// readers (including prefix replay) transfer ownership to the decode pipeline.
func (h *Handler) openThumbnailSource(ctx context.Context, obj repository.Object, version string) (service.ThumbnailSource, error) {
	source, err := h.openUnencodedThumbnailSource(ctx, obj, version)
	if err != nil {
		return service.ThumbnailSource{}, err
	}
	sniff, err := thumbnailMediaType(source.Object.ContentType)
	if err != nil {
		closeSniffSource(source.Reader)
		return service.ThumbnailSource{}, err
	}
	if sniff {
		source.Reader, err = sniffThumbnailSource(ctx, source.Reader)
		if err != nil {
			return service.ThumbnailSource{}, err
		}
	}
	return source, nil
}
