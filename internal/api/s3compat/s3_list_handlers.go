package s3compat

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func (h *Handler) listObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.URL.Query().Get("list-type") == "2" {
		h.listObjectsV2(w, r, bucket)
		return
	}
	h.listObjectsV1(w, r, bucket)
}

func (h *Handler) listObjectsV2(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	rawToken := q.Get("continuation-token")
	marker := ""
	if rawToken != "" {
		marker = rawToken
		if decoded, err := base64.StdEncoding.DecodeString(rawToken); err == nil && len(decoded) > 0 {
			marker = string(decoded)
		}
	} else {
		marker = q.Get("start-after")
	}
	maxKeys := s3PageLimit(q.Get("max-keys"), 1000)
	tagKey := q.Get("tag-key")
	tagValue := q.Get("tag-value")
	ctx := r.Context()
	tenant := mw.TenantFrom(ctx)

	var page repository.ListPage
	var err error
	if maxKeys > 0 {
		if tagKey != "" {
			page, err = h.svc.ListByTag(ctx, tenant, bucket, prefix, marker, maxKeys, tagKey, tagValue)
		} else {
			page, err = h.svc.List(ctx, tenant, bucket, prefix, marker, maxKeys)
		}
	}
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listBucketResult{
		Xmlns:             s3Namespace,
		Name:              bucket,
		Prefix:            prefix,
		KeyCount:          len(page.Objects),
		MaxKeys:           maxKeys,
		IsTruncated:       page.HasMore,
		ContinuationToken: rawToken,
		StartAfter:        q.Get("start-after"),
	}
	if page.HasMore {
		out.NextContinuationToken = base64.StdEncoding.EncodeToString([]byte(page.NextMarker))
	}
	for _, o := range page.Objects {
		out.Contents = append(out.Contents, listContent{
			Key:          o.Key,
			LastModified: o.UpdatedAt.UTC(),
			ETag:         `"` + o.ETag + `"`,
			Size:         o.Size,
			StorageClass: service.StorageClassOrDefault(o.StorageClass),
		})
	}
	writeListResult(w, out)
}

func (h *Handler) listObjectsV1(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	marker := q.Get("marker")
	maxKeys := s3PageLimit(q.Get("max-keys"), 1000)
	page := repository.ListPage{}
	var err error
	if maxKeys > 0 {
		page, err = h.svc.List(
			r.Context(), mw.TenantFrom(r.Context()), bucket, prefix, marker, maxKeys,
		)
	}
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listBucketResultV1{
		Xmlns:       s3Namespace,
		Name:        bucket,
		Prefix:      prefix,
		Marker:      marker,
		MaxKeys:     maxKeys,
		IsTruncated: page.HasMore,
	}
	if page.HasMore {
		out.NextMarker = page.NextMarker
	}
	for _, o := range page.Objects {
		out.Contents = append(out.Contents, listContent{
			Key:          o.Key,
			LastModified: o.UpdatedAt.UTC(),
			ETag:         `"` + o.ETag + `"`,
			Size:         o.Size,
			StorageClass: service.StorageClassOrDefault(o.StorageClass),
		})
	}
	writeListResult(w, out)
}

func writeListResult(w http.ResponseWriter, out any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(out)
}
