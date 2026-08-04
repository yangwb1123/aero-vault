package s3compat

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// ── Bucket Sub-resource Dispatch ───────────────────────────────────────────

func (h *Handler) dispatchBucketSubresource(w http.ResponseWriter, r *http.Request, bucket string, q url.Values) bool {
	if owner := r.Header.Get("x-amz-expected-bucket-owner"); owner != "" && owner != mw.TenantFrom(r.Context()) {
		writeS3Error(w, r, service.ErrForbidden)
		return true
	}
	switch {
	case q.Has("versioning"):
		h.dispatchVersioning(w, r, bucket)
		return true
	case q.Has("lifecycle"):
		h.dispatchLifecycle(w, r, bucket)
		return true
	case q.Has("object-lock"):
		h.dispatchObjectLock(w, r, bucket)
		return true
	case q.Has("acl"):
		h.dispatchACL(w, r, bucket)
		return true
	case q.Has("location"):
		h.getBucketLocation(w, r, bucket)
		return true
	case q.Has("versions"):
		h.listObjectVersions(w, r, bucket)
		return true
	case q.Has("policy"):
		h.dispatchBucketPolicy(w, r, bucket)
		return true
	case q.Has("logging"):
		h.dispatchBucketLogging(w, r, bucket)
		return true
	case q.Has("notification"):
		h.dispatchBucketNotifications(w, r, bucket)
		return true
	case q.Has("accelerate"):
		h.dispatchBucketAccelerate(w, r, bucket)
		return true
	case q.Has("encryption"):
		h.dispatchBucketEncryption(w, r, bucket)
		return true
	case q.Has("website"):
		h.dispatchBucketWebsite(w, r, bucket)
		return true
	case q.Has("tagging"):
		h.dispatchBucketTagging(w, r, bucket)
		return true
	case q.Has("cors"):
		h.dispatchBucketCORS(w, r, bucket)
		return true
	}
	return false
}

func (h *Handler) dispatchBucketCORS(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodGet:
		h.getBucketCORS(w, r, bucket)
	case http.MethodPut:
		h.putBucketCORS(w, r, bucket)
	case http.MethodDelete:
		h.deleteBucketCORS(w, r, bucket)
	default:
		writeS3Error(w, r, service.ErrInvalidArgs)
	}
}

func (h *Handler) dispatchBucketTagging(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodGet:
		h.getBucketTagging(w, r, bucket)
	case http.MethodPut:
		h.putBucketTagging(w, r, bucket)
	case http.MethodDelete:
		h.deleteBucketTagging(w, r, bucket)
	default:
		writeS3Error(w, r, service.ErrInvalidArgs)
	}
}

func (h *Handler) dispatchBucketWebsite(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodGet:
		h.getBucketWebsite(w, r, bucket)
	case http.MethodPut:
		h.putBucketWebsite(w, r, bucket)
	case http.MethodDelete:
		h.deleteBucketWebsite(w, r, bucket)
	default:
		writeS3Error(w, r, service.ErrInvalidArgs)
	}
}

func (h *Handler) dispatchBucketEncryption(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodGet:
		h.getBucketEncryption(w, r, bucket)
	case http.MethodPut:
		h.putBucketEncryption(w, r, bucket)
	case http.MethodDelete:
		h.deleteBucketEncryption(w, r, bucket)
	default:
		writeS3Error(w, r, service.ErrInvalidArgs)
	}
}

func (h *Handler) getBucketEncryption(w http.ResponseWriter, r *http.Request, bucket string) {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if cfg.SSEAlgorithm == "" {
		// No encryption configured → return ServerSideEncryptionConfigurationNotFoundError
		writeS3Error(w, r, service.ErrNotFound)
		return
	}
	out := serverSideEncryptionConfiguration{
		XMLNS: s3Namespace,
		Rules: []serverSideEncryptionRule{{
			Apply: serverSideEncryptionApply{
				SSEAlgorithm:   cfg.SSEAlgorithm,
				KMSMasterKeyID: cfg.SSEKMSKeyId,
			},
		}},
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) putBucketEncryption(w http.ResponseWriter, r *http.Request, bucket string) {
	var in serverSideEncryptionConfiguration
	if err := xml.NewDecoder(r.Body).Decode(&in); err != nil {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	if len(in.Rules) == 0 {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	alg := in.Rules[0].Apply.SSEAlgorithm
	kmsKey := in.Rules[0].Apply.KMSMasterKeyID
	if alg != "AES256" && alg != "aws:kms" {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	if alg == "aws:kms" && kmsKey == "" {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	if err := h.svc.SetBucketEncryption(r.Context(), mw.TenantFrom(r.Context()), bucket, alg, kmsKey); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteBucketEncryption(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := h.svc.DeleteBucketEncryption(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) dispatchBucketLogging(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodGet:
		h.getBucketLogging(w, r, bucket)
	case http.MethodPut:
		h.putBucketLogging(w, r, bucket)
	case http.MethodDelete:
		h.deleteBucketLogging(w, r, bucket)
	default:
		writeS3Error(w, r, service.ErrInvalidArgs)
	}
}

func (h *Handler) dispatchBucketPolicy(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodPut:
		h.putBucketPolicy(w, r, bucket)
	case http.MethodDelete:
		h.deleteBucketPolicy(w, r, bucket)
	default:
		h.getBucketPolicy(w, r, bucket)
	}
}

func (h *Handler) getBucketPolicy(w http.ResponseWriter, r *http.Request, bucket string) {
	policy, err := h.svc.GetBucketPolicy(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if policy == "" {
		writeS3Error(w, r, errNoSuchBucketPolicy)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(policy))
}

func (h *Handler) putBucketPolicy(w http.ResponseWriter, r *http.Request, bucket string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, bucketConfigBodyLimit))
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if err := h.svc.SetBucketPolicy(r.Context(), mw.TenantFrom(r.Context()), bucket, string(body)); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteBucketPolicy(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := h.svc.SetBucketPolicy(r.Context(), mw.TenantFrom(r.Context()), bucket, ""); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) dispatchVersioning(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method == http.MethodPut {
		h.putBucketVersioning(w, r, bucket)
	} else {
		h.getBucketVersioning(w, r, bucket)
	}
}

func (h *Handler) dispatchObjectLock(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method == http.MethodPut {
		h.putBucketObjectLock(w, r, bucket)
	} else {
		h.getBucketObjectLock(w, r, bucket)
	}
}

func (h *Handler) dispatchACL(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method == http.MethodPut {
		h.putBucketACL(w, r, bucket)
	} else {
		h.getBucketACL(w, r, bucket)
	}
}

func (h *Handler) dispatchLifecycle(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodPut:
		h.putBucketLifecycle(w, r, bucket)
	case http.MethodDelete:
		h.deleteBucketLifecycle(w, r, bucket)
	default:
		h.getBucketLifecycle(w, r, bucket)
	}
}

// ── Bucket CRUD ─────────────────────────────────────────────────────────────

func (h *Handler) headBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	exists, err := h.svc.HeadBucket(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) createBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	acl := r.Header.Get("x-amz-acl")
	if err := service.ValidateACL(acl); err != nil {
		writeS3Error(w, r, err)
		return
	}
	if err := h.svc.CreateBucket(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	if acl != "" {
		if err := h.svc.SetBucketACL(r.Context(), mw.TenantFrom(r.Context()), bucket, acl); err != nil {
			writeS3Error(w, r, err)
			return
		}
	}
	w.Header().Set("Location", "/"+bucket)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	tenant := mw.TenantFrom(r.Context())
	exists, err := h.svc.HeadBucket(r.Context(), tenant, bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if !exists {
		writeS3Error(w, r, errNoSuchBucket)
		return
	}
	keys, _, _, err := h.svc.ListVersionKeys(r.Context(), tenant, bucket, "", "", 1)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	hasUploads, err := h.svc.BucketHasMultipartUploads(r.Context(), tenant, bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if len(keys) != 0 || hasUploads {
		writeS3Error(w, r, errBucketNotEmpty)
		return
	}
	if err := h.svc.DeleteBucket(r.Context(), tenant, bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Bucket CORS ─────────────────────────────────────────────────────────────

func (h *Handler) getBucketCORS(w http.ResponseWriter, r *http.Request, bucket string) {
	rules, err := h.svc.GetBucketCORS(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := corsConfiguration{Xmlns: s3Namespace}
	for _, rule := range rules {
		out.Rules = append(out.Rules, corsRule{
			AllowedOrigins: rule.AllowedOrigins,
			AllowedMethods: rule.AllowedMethods,
			AllowedHeaders: rule.AllowedHeaders,
			ExposeHeaders:  rule.ExposeHeaders,
			MaxAgeSeconds:  rule.MaxAgeSeconds,
		})
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) putBucketCORS(w http.ResponseWriter, r *http.Request, bucket string) {
	var input corsInput
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	if err := xml.Unmarshal(body, &input); err != nil {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	var rules []repository.CORSRule
	for _, r := range input.Rules {
		rules = append(rules, repository.CORSRule{
			AllowedOrigins: r.AllowedOrigins,
			AllowedMethods: r.AllowedMethods,
			AllowedHeaders: r.AllowedHeaders,
			ExposeHeaders:  r.ExposeHeaders,
			MaxAgeSeconds:  r.MaxAgeSeconds,
		})
	}
	if err := h.svc.SetBucketCORS(r.Context(), mw.TenantFrom(r.Context()), bucket, rules); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteBucketCORS(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := h.svc.DeleteBucketCORS(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Bucket Logging ──────────────────────────────────────────────────────────

func (h *Handler) getBucketLogging(w http.ResponseWriter, r *http.Request, bucket string) {
	cfg, err := h.svc.GetBucketLogging(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	status := bucketLoggingStatus{
		Logging: loggingEnabled{},
	}
	if cfg.Enabled {
		status.Logging.TargetBucket = cfg.Target
		status.Logging.TargetPrefix = cfg.Prefix
	}
	writeXML(w, http.StatusOK, status)
}

func (h *Handler) putBucketLogging(w http.ResponseWriter, r *http.Request, bucket string) {
	var in bucketLoggingStatus
	if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &in); err != nil {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	if in.Logging.TargetBucket == "" {
		if err := h.svc.DeleteBucketLogging(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
			writeS3Error(w, r, err)
			return
		}
	} else {
		if err := h.svc.SetBucketLogging(r.Context(), mw.TenantFrom(r.Context()), bucket, in.Logging.TargetBucket, in.Logging.TargetPrefix); err != nil {
			writeS3Error(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}
