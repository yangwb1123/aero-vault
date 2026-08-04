package s3compat

import (
	"net/http"
	"strings"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func (h *Handler) listMultipartUploads(w http.ResponseWriter, r *http.Request, bucket string) {
	query := r.URL.Query()
	keyMarker := query.Get("key-marker")
	uploadMarker := query.Get("upload-id-marker")
	maxUploads := s3PageLimit(query.Get("max-uploads"), 1000)
	out := listMultipartUploadsResult{
		Xmlns: s3Namespace, Bucket: bucket, Prefix: query.Get("prefix"),
		KeyMarker: keyMarker, UploadIDMarker: uploadMarker, MaxUploads: maxUploads,
	}
	if maxUploads == 0 {
		writeXML(w, http.StatusOK, out)
		return
	}
	uploads, hasMore, err := h.multipartUploadPage(
		r, bucket, out.Prefix, keyMarker, uploadMarker, maxUploads,
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if hasMore {
		out.IsTruncated = true
		last := uploads[len(uploads)-1]
		out.NextKeyMarker = last.Key
		out.NextUploadIDMarker = last.ID
	}
	for _, upload := range uploads {
		out.Uploads = append(out.Uploads, uploadListItem{
			Key: upload.Key, UploadID: upload.ID, Initiated: upload.CreatedAt.UTC(),
		})
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) multipartUploadPage(
	r *http.Request,
	bucket, prefix, keyMarker, uploadMarker string,
	limit int,
) ([]repository.Upload, bool, error) {
	const scanSize = 1000
	var matched []repository.Upload
	for len(matched) <= limit {
		page, err := h.svc.ListMultipartUploads(
			r.Context(), mw.TenantFrom(r.Context()), bucket,
			keyMarker, uploadMarker, scanSize,
		)
		if err != nil {
			return nil, false, err
		}
		for _, upload := range page {
			if strings.HasPrefix(upload.Key, prefix) {
				matched = append(matched, upload)
				if len(matched) > limit {
					return matched[:limit], true, nil
				}
			}
		}
		if len(page) < scanSize {
			return matched, false, nil
		}
		last := page[len(page)-1]
		if last.Key == keyMarker && last.ID == uploadMarker {
			return matched, false, nil
		}
		keyMarker, uploadMarker = last.Key, last.ID
	}
	return matched, false, nil
}
