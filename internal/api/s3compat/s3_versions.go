package s3compat

import (
	"net/http"
	"strconv"
	"strings"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// listObjectVersions applies max-keys to the combined number of versions and
// delete markers, as required by S3. Continuation resumes within a key when a
// key has more versions than fit in one response.
func (h *Handler) listObjectVersions(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	keyMarker := q.Get("key-marker")
	versionMarker := q.Get("version-id-marker")
	maxKeys := s3PageLimit(q.Get("max-keys"), 1000)
	out := listVersionsResult{
		Xmlns: s3Namespace, Name: bucket, Prefix: prefix,
		KeyMarker: keyMarker, VersionIdMarker: versionMarker, MaxKeys: maxKeys,
	}
	if versionMarker != "" && keyMarker == "" {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	if versionMarker != "" && !strings.HasPrefix(keyMarker, prefix) {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	if maxKeys == 0 {
		writeXML(w, http.StatusOK, out)
		return
	}

	tenant := mw.TenantFrom(r.Context())
	keys, moreKeys, err := h.versionPageKeys(r, tenant, bucket, prefix, keyMarker, versionMarker, maxKeys)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if err := h.appendVersionPage(r, tenant, bucket, keys, versionMarker, moreKeys, &out); err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeXML(w, http.StatusOK, out)
}

func s3PageLimit(raw string, defaultValue int) int {
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return defaultValue
	}
	if value > 1000 {
		return 1000
	}
	return value
}

func (h *Handler) versionPageKeys(
	r *http.Request,
	tenant, bucket, prefix, keyMarker, versionMarker string,
	limit int,
) ([]string, bool, error) {
	keys := make([]string, 0, limit+1)
	if versionMarker != "" && strings.HasPrefix(keyMarker, prefix) {
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

func (h *Handler) appendVersionPage(
	r *http.Request,
	tenant, bucket string,
	keys []string,
	firstVersionMarker string,
	moreKeys bool,
	out *listVersionsResult,
) error {
	remaining := out.MaxKeys
	for keyIndex, key := range keys {
		marker := ""
		if keyIndex == 0 {
			marker = firstVersionMarker
		}
		page, err := h.svc.ListObjectVersionsWithOpts(r.Context(), tenant, bucket, key, repository.VersionListOpts{
			VersionIDMarker: marker,
			Limit:           remaining,
		})
		if err != nil {
			return err
		}
		h.appendVersions(out, page.Versions, marker != "")
		remaining -= len(page.Versions)
		more := page.HasMore || keyIndex+1 < len(keys) || moreKeys
		if (remaining == 0 && more) || page.HasMore {
			out.IsTruncated = true
			out.NextKeyMarker = key
			if len(page.Versions) > 0 {
				out.NextVersionIdMarker = page.Versions[len(page.Versions)-1].VersionID
			} else {
				out.NextVersionIdMarker = marker
			}
			return nil
		}
	}
	return nil
}

func (h *Handler) appendVersions(out *listVersionsResult, versions []repository.Object, startsAfter bool) {
	for index, version := range versions {
		isLatest := index == 0 && !startsAfter
		if service.IsDeleteMarker(version) {
			out.DeleteMarkers = append(out.DeleteMarkers, deleteMarkerEntry{
				Key: version.Key, VersionID: version.VersionID,
				IsLatest: isLatest, LastModified: version.UpdatedAt.UTC(),
			})
			continue
		}
		out.Versions = append(out.Versions, versionEntry{
			Key: version.Key, VersionID: version.VersionID,
			IsLatest: isLatest, LastModified: version.UpdatedAt.UTC(),
			ETag: `"` + version.ETag + `"`, Size: version.Size,
			StorageClass: service.StorageClassOrDefault(version.StorageClass),
		})
	}
}
