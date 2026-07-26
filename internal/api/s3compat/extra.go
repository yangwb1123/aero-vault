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
	"github.com/aero-vault/aero-vault/internal/repository"
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
	if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &in); err != nil {
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

// --- Object Legal Hold & Retention (S3 Object Lock) -------------------------

func (h *Handler) getObjectLegalHold(w http.ResponseWriter, r *http.Request, bucket, key string) {
	tenant := mw.TenantFrom(r.Context())
	obj, err := h.svc.Stat(r.Context(), tenant, bucket, key)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	status := "OFF"
	onHold, _ := h.svc.Repo().ObjectHasLegalHold(r.Context(), obj.ID)
	if onHold {
		status = "ON"
	}
	writeXML(w, http.StatusOK, objectLegalHold{Xmlns: s3Namespace, Status: status})
}

func (h *Handler) putObjectLegalHold(w http.ResponseWriter, r *http.Request, bucket, key string) {
	tenant := mw.TenantFrom(r.Context())
	status := r.Header.Get("x-amz-object-lock-legal-hold")
	if status == "" {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	obj, err := h.svc.Stat(r.Context(), tenant, bucket, key)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if strings.EqualFold(status, "ON") {
		err = h.svc.PutLegalHold(r.Context(), tenant, bucket, key, obj.VersionID, "s3 api", tenant)
	} else {
		err = h.svc.RemoveLegalHold(r.Context(), tenant, bucket, key, obj.VersionID)
	}
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) getObjectRetention(w http.ResponseWriter, r *http.Request, bucket, key string) {
	writeXML(w, http.StatusOK, objectRetention{Xmlns: s3Namespace, Mode: "GOVERNANCE", RetainUntilDate: ""})
}

func (h *Handler) putObjectRetention(w http.ResponseWriter, r *http.Request, bucket, key string) {
	w.WriteHeader(http.StatusOK)
}

// --- Multipart --------------------------------------------------------------

func (h *Handler) createMultipartUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	opts := service.PutOptions{
		ContentType: r.Header.Get("Content-Type"),
		Metadata:    extractMetaHeaders(r.Header),
	}
	if r.URL.Query().Has("tagging") {
		var in tagging
		if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &in); err == nil {
			tags := make(map[string]string, len(in.TagSet))
			for _, t := range in.TagSet {
				tags[t.Key] = t.Value
			}
			opts.Tags = tags
		}
	}
	up, err := h.svc.InitMultipart(r.Context(), mw.TenantFrom(r.Context()), bucket, key, opts)
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

func (h *Handler) uploadPartCopy(w http.ResponseWriter, r *http.Request, bucket, dstKey, copySource, uploadID string, partNumber int) {
	if partNumber < 1 || partNumber > 10000 {
		writeS3Error(w, r, fmt.Errorf("%w: partNumber must be between 1 and 10000", service.ErrInvalidArgs))
		return
	}
	srcBucket, srcKey, ok := parseCopySource(copySource)
	if !ok {
		writeS3Error(w, r, fmt.Errorf("%w: invalid x-amz-copy-source", service.ErrInvalidArgs))
		return
	}
	_ = bucket // dst bucket, same as src for now
	tenant := mw.TenantFrom(r.Context())

	// Parse optional byte range from x-amz-copy-source-range.
	rangeHeader := r.Header.Get("x-amz-copy-source-range")
	var srcOffset, length int64 = -1, 0
	if rangeHeader != "" {
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &srcOffset, &length); err != nil {
			writeS3Error(w, r, fmt.Errorf("%w: invalid x-amz-copy-source-range", service.ErrInvalidArgs))
			return
		}
		length = length - srcOffset + 1
	}

	// If no range specified, get source object size to know the part length.
	if rangeHeader == "" {
		src, err := h.svc.Stat(r.Context(), tenant, srcBucket, srcKey)
		if err != nil {
			writeS3Error(w, r, err)
			return
		}
		length = src.Size
	}

	part, err := h.svc.UploadPartCopy(r.Context(), uploadID, int32(partNumber), srcKey, srcOffset, length)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+part.ETag+`"`)
	writeXML(w, http.StatusOK, copyObjectResult{
		Xmlns: s3Namespace,
		ETag:  part.ETag,
	})
}

func (h *Handler) completeMultipartUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	uploadID := r.URL.Query().Get("uploadId")

	// Parse the client-supplied part manifest and verify ETags against the
	// server's stored parts before completing. This catches bit rot / partial
	// writes that could otherwise cause silent data corruption.
	var manifest completeMultipartUpload
	if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &manifest); err != nil {
		writeS3Error(w, r, errMalformedXML)
		return
	}

	// Convert client parts to the format expected by the service layer.
	var clientParts []repository.PartRecord
	for _, p := range manifest.Parts {
		clientParts = append(clientParts, repository.PartRecord{
			PartNumber: p.PartNumber,
			ETag:       p.ETag,
		})
	}

	obj, err := h.svc.CompleteMultipartWithParts(r.Context(), uploadID, clientParts)
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
	// ListParts returns all parts ordered by part_number ASC; paginate in-handler
	// per the S3 API (part-number-marker = exclusive start, max-parts = page size,
	// default/cap 1000).
	pnm, _ := strconv.Atoi(r.URL.Query().Get("part-number-marker")) // empty/invalid -> 0
	marker := int32(pnm)
	maxParts, _ := strconv.Atoi(r.URL.Query().Get("max-parts"))
	if maxParts <= 0 || maxParts > 1000 {
		maxParts = 1000
	}
	out := listPartsResult{
		Xmlns: s3Namespace, Bucket: bucket, Key: key, UploadID: uploadID,
		StorageClass: "STANDARD", PartNumberMarker: marker, MaxParts: maxParts,
	}
	for _, p := range parts {
		if p.PartNumber <= marker {
			continue
		}
		if len(out.Parts) >= maxParts {
			out.IsTruncated = true
			break
		}
		out.Parts = append(out.Parts, listPartItem{PartNumber: p.PartNumber, ETag: `"` + p.ETag + `"`, Size: p.Size})
		out.NextPartNumberMarker = p.PartNumber
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) listMultipartUploads(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	keyMarker := q.Get("key-marker")
	uploadIDMarker := q.Get("upload-id-marker")
	maxUploads, _ := strconv.Atoi(q.Get("max-uploads")) // empty/invalid -> 0 -> clamped below
	if maxUploads <= 0 || maxUploads > 1000 {
		maxUploads = 1000
	}
	// Fetch one extra to detect truncation (results are ordered by key,upload_id).
	ups, err := h.svc.Repo().ListUploads(r.Context(), mw.TenantFrom(r.Context()), bucket, keyMarker, uploadIDMarker, maxUploads+1)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listMultipartUploadsResult{
		Xmlns: s3Namespace, Bucket: bucket, Prefix: q.Get("prefix"),
		KeyMarker: keyMarker, UploadIDMarker: uploadIDMarker, MaxUploads: maxUploads,
	}
	if len(ups) > maxUploads {
		ups = ups[:maxUploads]
		out.IsTruncated = true
		if n := len(ups); n > 0 {
			out.NextKeyMarker = ups[n-1].Key
			out.NextUploadIDMarker = ups[n-1].ID
		}
	}
	for _, u := range ups {
		out.Uploads = append(out.Uploads, uploadListItem{Key: u.Key, UploadID: u.ID, Initiated: u.CreatedAt.UTC()})
	}
	writeXML(w, http.StatusOK, out)
}

// --- Batch delete -----------------------------------------------------------

func (h *Handler) deleteObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	var in deleteRequest
	if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &in); err != nil {
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
