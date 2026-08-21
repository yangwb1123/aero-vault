package s3compat

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
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
	delimiter := q.Get("delimiter")
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

	fetch := func(cursor string, limit int) (repository.ListPage, error) {
		if tagKey != "" {
			return h.svc.ListByTag(ctx, tenant, bucket, prefix, cursor, limit, tagKey, tagValue)
		}
		return h.svc.List(ctx, tenant, bucket, prefix, cursor, limit)
	}
	page, err := loadObjectListPage(prefix, delimiter, marker, maxKeys, fetch)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listBucketResult{
		Xmlns:             s3Namespace,
		Name:              bucket,
		Prefix:            prefix,
		KeyCount:          page.keyCount(),
		MaxKeys:           maxKeys,
		Delimiter:         delimiter,
		IsTruncated:       page.HasMore,
		ContinuationToken: rawToken,
		StartAfter:        q.Get("start-after"),
		Contents:          listContents(page.Objects),
		CommonPrefixes:    page.CommonPrefixes,
	}
	if page.HasMore {
		out.NextContinuationToken = base64.StdEncoding.EncodeToString([]byte(page.NextMarker))
	}
	writeListResult(w, out)
}

func (h *Handler) listObjectsV1(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	marker := q.Get("marker")
	maxKeys := s3PageLimit(q.Get("max-keys"), 1000)
	ctx := r.Context()
	tenant := mw.TenantFrom(ctx)
	fetch := func(cursor string, limit int) (repository.ListPage, error) {
		return h.svc.List(ctx, tenant, bucket, prefix, cursor, limit)
	}
	page, err := loadObjectListPage(prefix, delimiter, marker, maxKeys, fetch)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listBucketResultV1{
		Xmlns:          s3Namespace,
		Name:           bucket,
		Prefix:         prefix,
		Marker:         marker,
		MaxKeys:        maxKeys,
		Delimiter:      delimiter,
		IsTruncated:    page.HasMore,
		Contents:       listContents(page.Objects),
		CommonPrefixes: page.CommonPrefixes,
	}
	if page.HasMore {
		out.NextMarker = page.NextMarker
	}
	writeListResult(w, out)
}

func writeListResult(w http.ResponseWriter, out any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(out)
}
