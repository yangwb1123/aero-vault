package rest

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type bucketVersionEntry struct {
	Key          string `json:"key"`
	VersionID    string `json:"version_id"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag,omitempty"`
	IsLatest     bool   `json:"is_latest"`
	DeleteMarker bool   `json:"delete_marker,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

type bucketVersionsResponse struct {
	Versions            []bucketVersionEntry `json:"versions"`
	HasMore             bool                 `json:"has_more"`
	NextKeyMarker       string               `json:"next_key_marker,omitempty"`
	NextVersionIDMarker string               `json:"next_version_id_marker,omitempty"`
}

// ListBucketVersions returns historical versions and delete markers. max-keys
// applies to their combined count, including when a page ends within one key.
func (h *Handler) ListBucketVersions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	keyMarker := q.Get("key-marker")
	versionMarker := q.Get("version-id-marker")
	limit := restVersionPageLimit(q.Get("max-keys"))
	out := bucketVersionsResponse{Versions: []bucketVersionEntry{}}
	if versionMarker != "" &&
		(keyMarker == "" || !strings.HasPrefix(keyMarker, prefix)) {
		h.writeError(w, r, service.ErrInvalidArgs)
		return
	}
	if limit == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	tenant := mw.TenantFrom(r.Context())
	bucket := chi.URLParam(r, "bucket")
	keys, moreKeys, err := h.restVersionPageKeys(
		r, tenant, bucket, prefix, keyMarker, versionMarker, limit,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.appendRESTVersionPage(
		r, tenant, bucket, keys, versionMarker, moreKeys, limit, &out,
	); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func restVersionPageLimit(raw string) int {
	if raw == "" {
		return 1000
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 1000
	}
	if value > 1000 {
		return 1000
	}
	return value
}

func (h *Handler) restVersionPageKeys(
	r *http.Request,
	tenant, bucket, prefix, keyMarker, versionMarker string,
	limit int,
) ([]string, bool, error) {
	keys := make([]string, 0, limit+1)
	if versionMarker != "" {
		keys = append(keys, keyMarker)
	}
	later, _, more, err := h.svc.ListVersionKeys(
		r.Context(), tenant, bucket, prefix, keyMarker, limit,
	)
	if err != nil {
		return nil, false, err
	}
	return append(keys, later...), more, nil
}

func (h *Handler) appendRESTVersionPage(
	r *http.Request,
	tenant, bucket string,
	keys []string,
	firstVersionMarker string,
	moreKeys bool,
	limit int,
	out *bucketVersionsResponse,
) error {
	remaining := limit
	for keyIndex, key := range keys {
		marker := ""
		if keyIndex == 0 {
			marker = firstVersionMarker
		}
		page, err := h.svc.ListObjectVersionsWithOpts(
			r.Context(), tenant, bucket, key,
			repository.VersionListOpts{
				VersionIDMarker: marker,
				Limit:           remaining,
			},
		)
		if err != nil {
			return err
		}
		appendRESTVersions(out, page.Versions, marker != "")
		remaining -= len(page.Versions)
		more := page.HasMore || keyIndex+1 < len(keys) || moreKeys
		if (remaining == 0 && more) || page.HasMore {
			setRESTVersionContinuation(out, key, marker, page.Versions)
			return nil
		}
	}
	return nil
}

func appendRESTVersions(
	out *bucketVersionsResponse,
	versions []repository.Object,
	startsAfter bool,
) {
	for index, version := range versions {
		out.Versions = append(out.Versions, bucketVersionEntry{
			Key:          version.Key,
			VersionID:    version.VersionID,
			Size:         version.Size,
			ETag:         version.ETag,
			IsLatest:     index == 0 && !startsAfter,
			DeleteMarker: service.IsDeleteMarker(version),
			UpdatedAt:    version.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
}

func setRESTVersionContinuation(
	out *bucketVersionsResponse,
	key, marker string,
	versions []repository.Object,
) {
	out.HasMore = true
	out.NextKeyMarker = key
	out.NextVersionIDMarker = marker
	if len(versions) > 0 {
		out.NextVersionIDMarker = versions[len(versions)-1].VersionID
	}
}
