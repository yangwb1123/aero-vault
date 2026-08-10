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
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// PostObject handles POST /{bucket}/{key+}: CreateMultipartUpload (?uploads) and
// CompleteMultipartUpload (?uploadId=...).
func (h *Handler) PostObject(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeS3Request(w, r) {
		return
	}
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
	srcBucket, srcKey, srcVersionID, ok := parseCopySource(copySource)
	if !ok {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}

	sourceSSEC, err := parseSSECRequest(r.Header, ssecCopyHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer sourceSSEC.clear()
	destinationSSEC, err := parseSSECRequest(r.Header, ssecHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer destinationSSEC.clear()
	replace := strings.EqualFold(r.Header.Get("x-amz-metadata-directive"), "REPLACE")
	replaceTags := strings.EqualFold(r.Header.Get("x-amz-tagging-directive"), "REPLACE")
	opts := service.PutOptions{}
	opts.ACL = r.Header.Get("x-amz-acl")
	opts.LegalHold, err = legalHoldFromHeader(r.Header)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if replace {
		opts.ContentType = r.Header.Get("Content-Type")
		opts.Metadata = extractMetaHeaders(r.Header)
		opts.ContentDisposition = r.Header.Get("Content-Disposition")
		opts.ContentEncoding = r.Header.Get("Content-Encoding")
	}
	if replaceTags {
		opts.Tags, err = parseTaggingHeader(r.Header)
		if err != nil {
			writeS3Error(w, r, err)
			return
		}
	}
	applyManagedSSEHeaders(r.Header, &opts)
	destinationSSEC.applyPutOptions(&opts)
	dst, err := h.svc.CopyObject(
		r.Context(), tenant, srcBucket, srcKey, srcVersionID, dstBucket, dstKey,
		sourceSSEC.readOptions(), opts, replace, replaceTags,
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	h.writeCurrentVersionHeader(w, r, dst)
	writeEncryptionHeaders(w, dst.Metadata)
	writeXML(w, http.StatusOK, copyObjectResult{
		Xmlns: s3Namespace, LastModified: dst.UpdatedAt.UTC(), ETag: `"` + dst.ETag + `"`,
	})
}

// parseCopySource splits "/bucket/key" or "bucket/key" (optionally URL-encoded,
// optionally with ?versionId) into bucket + key.
func parseCopySource(s string) (bucket, key, versionID string, ok bool) {
	s = strings.TrimPrefix(s, "/")
	rawPath, rawQuery, _ := strings.Cut(s, "?")
	if values, err := url.ParseQuery(rawQuery); err == nil {
		versionID = values.Get("versionId")
	}
	if dec, err := url.PathUnescape(rawPath); err == nil {
		rawPath = dec
	}
	parts := strings.SplitN(rawPath, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], versionID, true
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
	tags, err := tagsFromSet(in.TagSet)
	if err != nil {
		writeS3Error(w, r, err)
		return
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
	acl, err := cannedACLFromRequest(r)
	if err != nil {
		writeS3Error(w, r, err)
		return
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
	ssec, err := parseSSECRequest(r.Header, ssecHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer ssec.clear()
	opts := service.PutOptions{
		ContentType:        r.Header.Get("Content-Type"),
		ContentDisposition: r.Header.Get("Content-Disposition"),
		ContentEncoding:    r.Header.Get("Content-Encoding"),
		Metadata:           extractMetaHeaders(r.Header),
		ACL:                r.Header.Get("x-amz-acl"),
		StorageClass:       r.Header.Get("x-amz-storage-class"),
	}
	opts.LegalHold, err = legalHoldFromHeader(r.Header)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	applyManagedSSEHeaders(r.Header, &opts)
	ssec.applyPutOptions(&opts)
	opts.Tags, err = parseTaggingHeader(r.Header)
	if err != nil {
		writeS3Error(w, r, err)
		return
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
	writeEncryptionHeaders(w, up.Metadata)
	writeXML(w, http.StatusOK, initiateMultipartUploadResult{
		Xmlns: s3Namespace, Bucket: bucket, Key: key, UploadID: up.ID,
	})
}

func (h *Handler) uploadPart(
	w http.ResponseWriter, r *http.Request, bucket, key, uploadID string, partNumber int,
) {
	// S3 part numbers are 1..10000; reject out-of-range (e.g. a missing/0 value).
	if partNumber < 1 || partNumber > 10000 {
		writeS3Error(w, r, fmt.Errorf("%w: partNumber must be between 1 and 10000", service.ErrInvalidArgs))
		return
	}
	ssec, parseErr := parseSSECRequest(r.Header, ssecHeaderPrefix)
	if parseErr != nil {
		writeS3Error(w, r, parseErr)
		return
	}
	defer ssec.clear()
	part, err := h.svc.UploadPartFor(
		r.Context(),
		service.MultipartScope{TenantID: mw.TenantFrom(r.Context()), Bucket: bucket, Key: key},
		uploadID, int32(partNumber), r.Body, r.ContentLength, ssec.readOptions(),
	)
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
	srcBucket, srcKey, srcVersionID, ok := parseCopySource(copySource)
	if !ok {
		writeS3Error(w, r, fmt.Errorf("%w: invalid x-amz-copy-source", service.ErrInvalidArgs))
		return
	}
	tenant := mw.TenantFrom(r.Context())
	sourceSSEC, err := parseSSECRequest(r.Header, ssecCopyHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer sourceSSEC.clear()
	destinationSSEC, err := parseSSECRequest(r.Header, ssecHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer destinationSSEC.clear()

	srcOffset, length := int64(-1), int64(0)
	if rangeHeader := r.Header.Get("x-amz-copy-source-range"); rangeHeader != "" {
		srcOffset, length, err = parseCopySourceRange(rangeHeader)
		if err != nil {
			writeS3Error(w, r, fmt.Errorf("%w: invalid x-amz-copy-source-range", service.ErrInvalidArgs))
			return
		}
	}

	part, err := h.svc.UploadPartCopyFor(
		r.Context(), service.MultipartScope{TenantID: tenant, Bucket: bucket, Key: dstKey},
		uploadID, int32(partNumber), srcBucket, srcKey, srcVersionID, srcOffset, length,
		sourceSSEC.readOptions(), destinationSSEC.readOptions(),
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+part.ETag+`"`)
	writeXML(w, http.StatusOK, copyPartResult{
		Xmlns: s3Namespace, LastModified: time.Now().UTC(), ETag: `"` + part.ETag + `"`,
	})
}

func parseCopySourceRange(value string) (int64, int64, error) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(value), "bytes=")
	if !ok || strings.Count(raw, "-") != 1 {
		return 0, 0, service.ErrInvalidArgs
	}
	startRaw, endRaw, _ := strings.Cut(raw, "-")
	start, err := strconv.ParseInt(startRaw, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, service.ErrInvalidArgs
	}
	end, err := strconv.ParseInt(endRaw, 10, 64)
	if err != nil || end < start {
		return 0, 0, service.ErrInvalidArgs
	}
	return start, end - start + 1, nil
}

func (h *Handler) completeMultipartUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	uploadID := r.URL.Query().Get("uploadId")
	ssec, err := parseSSECRequest(r.Header, ssecHeaderPrefix)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	defer ssec.clear()

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

	obj, err := h.svc.CompleteMultipartWithPartsFor(
		r.Context(),
		service.MultipartScope{TenantID: mw.TenantFrom(r.Context()), Bucket: bucket, Key: key},
		uploadID, clientParts, ssec.readOptions(),
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	h.writeCurrentVersionHeader(w, r, obj)
	writeEncryptionHeaders(w, obj.Metadata)
	writeXML(w, http.StatusOK, completeMultipartUploadResult{
		Xmlns:    s3Namespace,
		Location: "/" + bucket + "/" + key,
		Bucket:   bucket,
		Key:      key,
		ETag:     `"` + obj.ETag + `"`,
	})
}

func (h *Handler) abortMultipartUpload(
	w http.ResponseWriter, r *http.Request, bucket, key, uploadID string,
) {
	err := h.svc.AbortMultipartFor(
		r.Context(),
		service.MultipartScope{TenantID: mw.TenantFrom(r.Context()), Bucket: bucket, Key: key},
		uploadID,
	)
	if err != nil && !errors.Is(err, service.ErrUploadNotFound) {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listParts(w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	parts, err := h.svc.ListMultipartParts(
		r.Context(),
		service.MultipartScope{TenantID: mw.TenantFrom(r.Context()), Bucket: bucket, Key: key},
		uploadID,
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	// ListParts returns all parts ordered by part_number ASC; paginate in-handler
	// per the S3 API (part-number-marker = exclusive start, max-parts = page size,
	// default/cap 1000).
	pnm, _ := strconv.Atoi(r.URL.Query().Get("part-number-marker")) // empty/invalid -> 0
	marker := int32(pnm)
	maxParts := s3PageLimit(r.URL.Query().Get("max-parts"), 1000)
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
		if !h.authorizeDelete(r.Context(), tenant, bucket, o.Key) {
			out.Errors = append(out.Errors, deleteErrItem{
				Key: o.Key, VersionID: o.VersionID, Code: "AccessDenied", Message: "Access denied.",
			})
			continue
		}
		versionID, deleteMarker, err := h.deleteS3Object(
			r.Context(), tenant, bucket, o.Key, o.VersionID,
		)
		switch {
		case err == nil, errors.Is(err, service.ErrNotFound):
			if !in.Quiet {
				out.Deleted = append(out.Deleted, deletedItem{
					Key: o.Key, VersionID: versionID, DeleteMarker: deleteMarker,
				})
			}
		default:
			out.Errors = append(out.Errors, deleteErrItem{
				Key: o.Key, VersionID: o.VersionID, Code: s3ErrorCode(err), Message: err.Error(),
			})
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
