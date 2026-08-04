package s3compat

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func (h *Handler) serveObjectVersion(
	w http.ResponseWriter,
	r *http.Request,
	tenant, bucket, key, versionID string,
	opts service.ReadOptions,
) {
	obj, err := h.svc.StatVersionWithOptions(
		r.Context(), tenant, bucket, key, versionID, opts,
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeVersionHeaders(w, obj.VersionID)
	if status := evalS3GetPreconditions(r, obj); status != 0 {
		writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
		w.WriteHeader(status)
		return
	}
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		offset, length, ok, unsatisfiable := service.ParseByteRange(rangeHeader, obj.Size)
		if unsatisfiable {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(obj.Size, 10))
			writeS3Error(w, r, service.ErrRangeNotSatisfiable)
			return
		}
		if ok {
			h.writeVersionRange(w, r, obj, versionID, offset, length, opts)
			return
		}
	}
	rc, obj, err := h.svc.GetVersionWithOptions(
		r.Context(), tenant, bucket, key, versionID, opts,
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer rc.Close()
	writeObjectHeaders(w, obj.ContentType, obj.Size, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
	writeVersionHeaders(w, obj.VersionID)
	writeEncryptionHeaders(w, obj.Metadata)
	writeS3ObjectMeta(w, obj.Metadata)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (h *Handler) writeVersionRange(
	w http.ResponseWriter,
	r *http.Request,
	obj repository.Object,
	versionID string,
	offset, length int64,
	opts service.ReadOptions,
) {
	rc, _, err := h.svc.GetVersionRangeWithOptions(
		r.Context(), obj.TenantID, obj.Bucket, obj.Key, versionID, offset, length, opts,
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer rc.Close()
	writeObjectHeaders(w, obj.ContentType, length, obj.ETag, obj.UpdatedAt.Format(http.TimeFormat), obj.StorageClass, obj.Metadata)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, obj.Size))
	writeVersionHeaders(w, obj.VersionID)
	writeEncryptionHeaders(w, obj.Metadata)
	writeS3ObjectMeta(w, obj.Metadata)
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.Copy(w, rc)
}

func writeVersionHeaders(w http.ResponseWriter, versionID string) {
	if versionID != "" {
		w.Header().Set("x-amz-version-id", versionID)
	}
}

func (h *Handler) writeCurrentVersionHeader(
	w http.ResponseWriter, r *http.Request, obj repository.Object,
) {
	cfg, err := h.svc.GetBucketConfig(
		r.Context(), obj.TenantID, obj.Bucket,
	)
	if err == nil && cfg.Versioning {
		writeVersionHeaders(w, obj.VersionID)
	}
}
