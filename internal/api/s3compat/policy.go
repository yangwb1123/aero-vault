package s3compat

import (
	"bytes"
	"io"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

type bucketPolicyActionRule struct {
	query        string
	getAction    string
	putAction    string
	deleteAction string
}

var bucketPolicyActionRules = []bucketPolicyActionRule{
	{query: "versioning", getAction: "s3:GetBucketVersioning", putAction: "s3:PutBucketVersioning"},
	{query: "lifecycle", getAction: "s3:GetLifecycleConfiguration", putAction: "s3:PutLifecycleConfiguration", deleteAction: "s3:PutLifecycleConfiguration"},
	{query: "object-lock", getAction: "s3:GetBucketObjectLockConfiguration", putAction: "s3:PutBucketObjectLockConfiguration"},
	{query: "acl", getAction: "s3:GetBucketAcl", putAction: "s3:PutBucketAcl"},
	{query: "location", getAction: "s3:GetBucketLocation"},
	{query: "versions", getAction: "s3:ListBucketVersions"},
	{query: "policy", getAction: "s3:GetBucketPolicy", putAction: "s3:PutBucketPolicy", deleteAction: "s3:DeleteBucketPolicy"},
	{query: "logging", getAction: "s3:GetBucketLogging", putAction: "s3:PutBucketLogging", deleteAction: "s3:PutBucketLogging"},
	{query: "notification", getAction: "s3:GetBucketNotification", putAction: "s3:PutBucketNotification", deleteAction: "s3:PutBucketNotification"},
	{query: "accelerate", getAction: "s3:GetAccelerateConfiguration", putAction: "s3:PutAccelerateConfiguration"},
	{query: "encryption", getAction: "s3:GetEncryptionConfiguration", putAction: "s3:PutEncryptionConfiguration", deleteAction: "s3:PutEncryptionConfiguration"},
	{query: "website", getAction: "s3:GetBucketWebsite", putAction: "s3:PutBucketWebsite", deleteAction: "s3:DeleteBucketWebsite"},
	{query: "tagging", getAction: "s3:GetBucketTagging", putAction: "s3:PutBucketTagging", deleteAction: "s3:PutBucketTagging"},
	{query: "uploads", getAction: "s3:ListBucketMultipartUploads"},
	{query: "cors", getAction: "s3:GetBucketCORS", putAction: "s3:PutBucketCORS", deleteAction: "s3:PutBucketCORS"},
	{query: "delete", putAction: "s3:DeleteObject", deleteAction: "s3:DeleteObject"},
}

// authorizeS3Request is the common policy gate for every S3 route.
func (h *Handler) authorizeS3Request(w http.ResponseWriter, r *http.Request) bool {
	bucket := chi.URLParam(r, "bucket")
	key := keyFromURL(r)
	if owner := r.Header.Get("x-amz-expected-bucket-owner"); owner != "" &&
		owner != mw.TenantFrom(r.Context()) {
		writeS3Error(w, r, service.ErrForbidden)
		return false
	}
	if !h.validatePolicyWrite(w, r, key) {
		return false
	}
	action := bucketPolicyAction(r)
	if key != "" {
		action = objectPolicyAction(r)
	}
	if !h.checkBucketPolicy(w, r, bucket, key, action) {
		return false
	}
	srcBucket, srcKey, _, ok := parseCopySource(r.Header.Get("x-amz-copy-source"))
	if r.Header.Get("x-amz-copy-source") != "" && ok {
		if !h.checkBucketPolicy(w, r, srcBucket, srcKey, "s3:GetObject") {
			return false
		}
	}
	// FR-2: fail-closed vault.file.delete gate — object-level DELETE only.
	// Runs before any delete-path service call; the bucket-policy
	// GetBucketConfig lookups above are read-only and precede by design (R1).
	if key != "" && action == "s3:DeleteObject" {
		if !h.authorizeDelete(r.Context(), mw.TenantFrom(r.Context()), bucket, key) {
			writeS3Error(w, r, service.ErrForbidden)
			return false
		}
	}
	return true
}

func (h *Handler) validatePolicyWrite(w http.ResponseWriter, r *http.Request, key string) bool {
	if key != "" || r.Method != http.MethodPut || !r.URL.Query().Has("policy") {
		return true
	}
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, bucketConfigBodyLimit+1))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	policy, parseErr := auth.ParsePolicy(string(raw))
	if err == nil && len(raw) <= bucketConfigBodyLimit && parseErr == nil && policy != nil {
		return true
	}
	h.logger.Warn("rejecting invalid bucket policy", "err", firstPolicyError(err, parseErr))
	writeS3Error(w, r, service.ErrInvalidArgs)
	return false
}

func firstPolicyError(readErr, parseErr error) error {
	if readErr != nil {
		return readErr
	}
	return parseErr
}

func (h *Handler) checkBucketPolicy(
	w http.ResponseWriter,
	r *http.Request,
	bucket, key, action string,
) bool {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.logger.Warn("bucket policy lookup failed; denying request", "bucket", bucket, "err", err)
		writeS3Error(w, r, service.ErrForbidden)
		return false
	}
	if cfg.Policy == "" {
		return true
	}
	policy, err := auth.ParsePolicy(cfg.Policy)
	if err != nil || policy == nil {
		h.logger.Warn("bucket policy parse failed; denying request", "bucket", bucket, "err", err)
		writeS3Error(w, r, service.ErrForbidden)
		return false
	}
	resource := s3ResourceARN(bucket, key)
	if auth.AllowedResource(policy, action, resource, sourceIP(r)) {
		return true
	}
	writeS3Error(w, r, service.ErrForbidden)
	return false
}

func sourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func s3ResourceARN(bucket, key string) string {
	resource := "arn:aws:s3:::" + bucket
	if key != "" {
		resource += "/" + key
	}
	return resource
}

func bucketPolicyAction(r *http.Request) string {
	for _, rule := range bucketPolicyActionRules {
		if r.URL.Query().Has(rule.query) {
			return actionForMethod(r.Method, rule)
		}
	}
	switch r.Method {
	case http.MethodPut:
		return "s3:CreateBucket"
	case http.MethodDelete:
		return "s3:DeleteBucket"
	case http.MethodPost:
		return "s3:DeleteObject"
	default:
		return "s3:ListBucket"
	}
}

func actionForMethod(method string, rule bucketPolicyActionRule) string {
	action := rule.getAction
	switch method {
	case http.MethodPut, http.MethodPost:
		action = rule.putAction
	case http.MethodDelete:
		action = rule.deleteAction
	}
	if action == "" {
		return rule.getAction
	}
	return action
}

func objectPolicyAction(r *http.Request) string {
	q := r.URL.Query()
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return objectReadPolicyAction(q)
	case http.MethodPut:
		return objectWritePolicyAction(q)
	case http.MethodDelete:
		if q.Has("tagging") {
			return "s3:DeleteObjectTagging"
		}
		if q.Get("uploadId") != "" {
			return "s3:AbortMultipartUpload"
		}
		return "s3:DeleteObject"
	default:
		return "s3:PutObject"
	}
}

func objectReadPolicyAction(q map[string][]string) string {
	switch {
	case hasQuery(q, "tagging"):
		return "s3:GetObjectTagging"
	case hasQuery(q, "acl"):
		return "s3:GetObjectAcl"
	case hasQuery(q, "legal-hold"):
		return "s3:GetObjectLegalHold"
	case hasQuery(q, "retention"):
		return "s3:GetObjectRetention"
	case queryValue(q, "uploadId") != "":
		return "s3:ListMultipartUploadParts"
	default:
		return "s3:GetObject"
	}
}

func objectWritePolicyAction(q map[string][]string) string {
	switch {
	case hasQuery(q, "tagging"):
		return "s3:PutObjectTagging"
	case hasQuery(q, "acl"):
		return "s3:PutObjectAcl"
	case hasQuery(q, "legal-hold"):
		return "s3:PutObjectLegalHold"
	case hasQuery(q, "retention"):
		return "s3:PutObjectRetention"
	case hasQuery(q, "restore"):
		return "s3:RestoreObject"
	default:
		return "s3:PutObject"
	}
}

func hasQuery(q map[string][]string, key string) bool {
	_, ok := q[key]
	return ok
}

func queryValue(q map[string][]string, key string) string {
	values := q[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
