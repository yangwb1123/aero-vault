package s3compat

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// PostObject handles POST /{bucket}/{key+}: CreateMultipartUpload (?uploads) and
// CompleteMultipartUpload (?uploadId=...).
func (h *Handler) PostObject(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case q.Has("uploads"):
		h.createMultipartUpload(w, r)
	case q.Get("uploadId") != "":
		h.completeMultipartUpload(w, r)
	default:
		writeS3Error(w, r, service.ErrInvalidArgs)
	}
}

// --- CopyObject -------------------------------------------------------------

// copyObject implements PUT with an x-amz-copy-source header. It streams the
// source object's bytes into the destination key. With metadata-directive COPY
// (default) the source content-type + user metadata are preserved; REPLACE uses
// the request headers.
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
	tenant := mw.TenantFrom(r.Context())
	srcBucket, srcKey, ok := parseCopySource(copySource)
	if !ok {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}

	rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer rc.Close()

	opts := service.PutOptions{ContentType: src.ContentType, Metadata: src.Metadata}
	if strings.EqualFold(r.Header.Get("x-amz-metadata-directive"), "REPLACE") {
		opts.ContentType = r.Header.Get("Content-Type")
		opts.Metadata = extractMetaHeaders(r.Header)
	}

	dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeXML(w, http.StatusOK, copyObjectResult{
		Xmlns: s3Namespace, LastModified: dst.UpdatedAt.UTC(), ETag: `"` + dst.ETag + `"`,
	})
}

// parseCopySource splits "/bucket/key" or "bucket/key" (optionally URL-encoded,
// optionally with ?versionId) into bucket + key.
func parseCopySource(s string) (bucket, key string, ok bool) {
	s = strings.TrimPrefix(s, "/")
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if dec, err := url.QueryUnescape(s); err == nil {
		s = dec
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// --- Object tagging ---------------------------------------------------------

func (h *Handler) getObjectTagging(w http.ResponseWriter, r *http.Request, bucket, key string) {
	obj, err := h.svc.Stat(r.Context(), mw.TenantFrom(r.Context()), bucket, key)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := tagging{Xmlns: s3Namespace}
	for k, v := range obj.Tags {
		out.TagSet = append(out.TagSet, s3Tag{Key: k, Value: v})
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) putObjectTagging(w http.ResponseWriter, r *http.Request, bucket, key string) {
	var in tagging
	if err := xml.NewDecoder(r.Body).Decode(&in); err != nil {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	tags := make(map[string]string, len(in.TagSet))
	for _, t := range in.TagSet {
		tags[t.Key] = t.Value
	}
	if err := h.svc.SetTags(r.Context(), mw.TenantFrom(r.Context()), bucket, key, tags); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteObjectTagging(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if err := h.svc.SetTags(r.Context(), mw.TenantFrom(r.Context()), bucket, key, map[string]string{}); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Object ACL -------------------------------------------------------------

func (h *Handler) getObjectACL(w http.ResponseWriter, r *http.Request, bucket, key string) {
	acl, err := h.svc.GetObjectACL(r.Context(), mw.TenantFrom(r.Context()), bucket, key)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeXML(w, http.StatusOK, cannedToPolicy(acl))
}

func (h *Handler) putObjectACL(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Canned ACL via the x-amz-acl header (the common form).
	acl := r.Header.Get("x-amz-acl")
	if acl == "" {
		acl = "private"
	}
	if err := h.svc.SetObjectACL(r.Context(), mw.TenantFrom(r.Context()), bucket, key, acl); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Multipart --------------------------------------------------------------

func (h *Handler) createMultipartUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	up, err := h.svc.InitMultipart(r.Context(), mw.TenantFrom(r.Context()), bucket, key, service.PutOptions{
		ContentType: r.Header.Get("Content-Type"),
		Metadata:    extractMetaHeaders(r.Header),
	})
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeXML(w, http.StatusOK, initiateMultipartUploadResult{
		Xmlns: s3Namespace, Bucket: bucket, Key: key, UploadID: up.ID,
	})
}

func (h *Handler) uploadPart(w http.ResponseWriter, r *http.Request, uploadID string, partNumber int) {
	// S3 part numbers are 1..10000; reject out-of-range (e.g. a missing/0 value).
	if partNumber < 1 || partNumber > 10000 {
		writeS3Error(w, r, fmt.Errorf("%w: partNumber must be between 1 and 10000", service.ErrInvalidArgs))
		return
	}
	part, err := h.svc.UploadPart(r.Context(), uploadID, int32(partNumber), r.Body, r.ContentLength)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+part.ETag+`"`)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) completeMultipartUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	uploadID := r.URL.Query().Get("uploadId")
	// The client-supplied part manifest is parsed for compatibility but the
	// server's persisted parts are authoritative.
	_ = xml.NewDecoder(r.Body).Decode(&completeMultipartUpload{})

	obj, err := h.svc.CompleteMultipart(r.Context(), uploadID)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	writeXML(w, http.StatusOK, completeMultipartUploadResult{
		Xmlns:    s3Namespace,
		Location: "/" + bucket + "/" + key,
		Bucket:   bucket,
		Key:      key,
		ETag:     `"` + obj.ETag + `"`,
	})
}

func (h *Handler) abortMultipartUpload(w http.ResponseWriter, r *http.Request, uploadID string) {
	if err := h.svc.AbortMultipart(r.Context(), uploadID); err != nil && !errors.Is(err, service.ErrUploadNotFound) {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listParts(w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	parts, err := h.svc.Repo().ListParts(r.Context(), uploadID)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listPartsResult{Xmlns: s3Namespace, Bucket: bucket, Key: key, UploadID: uploadID, StorageClass: "STANDARD"}
	for _, p := range parts {
		out.Parts = append(out.Parts, listPartItem{PartNumber: p.PartNumber, ETag: `"` + p.ETag + `"`, Size: p.Size})
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) listMultipartUploads(w http.ResponseWriter, r *http.Request, bucket string) {
	ups, err := h.svc.Repo().ListUploads(r.Context(), mw.TenantFrom(r.Context()), bucket, 1000)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listMultipartUploadsResult{Xmlns: s3Namespace, Bucket: bucket, Prefix: r.URL.Query().Get("prefix")}
	for _, u := range ups {
		out.Uploads = append(out.Uploads, uploadListItem{Key: u.Key, UploadID: u.ID, Initiated: u.CreatedAt.UTC()})
	}
	writeXML(w, http.StatusOK, out)
}

// --- Batch delete -----------------------------------------------------------

func (h *Handler) deleteObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	var in deleteRequest
	if err := xml.NewDecoder(r.Body).Decode(&in); err != nil {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	tenant := mw.TenantFrom(r.Context())
	out := deleteResult{Xmlns: s3Namespace}
	for _, o := range in.Objects {
		err := h.svc.Delete(r.Context(), tenant, bucket, o.Key, true)
		switch {
		case err == nil, errors.Is(err, service.ErrNotFound):
			if !in.Quiet {
				out.Deleted = append(out.Deleted, deletedItem{Key: o.Key})
			}
		default:
			out.Errors = append(out.Errors, deleteErrItem{Key: o.Key, Code: "InternalError", Message: err.Error()})
		}
	}
	writeXML(w, http.StatusOK, out)
}

// writeXML emits an XML document with the standard header.
func writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(v)
}

// partNumberOf parses the ?partNumber query value (0 if absent/invalid).
func partNumberOf(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))
	return n
}
