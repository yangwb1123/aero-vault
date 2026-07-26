package s3compat

import (
	"encoding/xml"
	"fmt"
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
		h.getBucketAccelerate(w, r, bucket)
		return true
	case q.Has("encryption"):
		h.dispatchBucketEncryption(w, r, bucket)
		return true
	}
	return false
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
				SSEAlgorithm:  cfg.SSEAlgorithm,
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
	if err := h.svc.CreateBucket(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	if acl := r.Header.Get("x-amz-acl"); acl != "" {
		_ = h.svc.SetBucketACL(r.Context(), mw.TenantFrom(r.Context()), bucket, acl)
	}
	w.Header().Set("Location", "/"+bucket)
	w.WriteHeader(http.StatusOK)
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
	w.WriteHeader(http.StatusOK)
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

func (h *Handler) deleteBucketLogging(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := h.svc.DeleteBucketLogging(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ── Bucket Notifications ────────────────────────────────────────────────────

func (h *Handler) dispatchBucketNotifications(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodGet:
		h.getBucketNotifications(w, r, bucket)
	case http.MethodPut:
		h.putBucketNotifications(w, r, bucket)
	default:
		h.deleteBucketNotifications(w, r, bucket)
	}
}

func (h *Handler) getBucketNotifications(w http.ResponseWriter, r *http.Request, bucket string) {
	rules, err := h.svc.GetBucketNotifications(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := notificationConfiguration{Xmlns: s3Namespace}
	for _, rule := range rules {
		switch {
		case rule.QueueARN != "":
			out.QueueConfigs = append(out.QueueConfigs, queueConfig{
				ID: rule.ID, Events: rule.Events, QueueARN: rule.QueueARN,
				Filter: filterFromKey(rule.FilterKey),
			})
		case rule.TopicARN != "":
			out.TopicConfigs = append(out.TopicConfigs, topicConfig{
				ID: rule.ID, Events: rule.Events, TopicARN: rule.TopicARN,
				Filter: filterFromKey(rule.FilterKey),
			})
		case rule.LambdaARN != "":
			out.LambdaConfigs = append(out.LambdaConfigs, lambdaConfig{
				ID: rule.ID, Events: rule.Events, LambdaARN: rule.LambdaARN,
				Filter: filterFromKey(rule.FilterKey),
			})
		}
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) putBucketNotifications(w http.ResponseWriter, r *http.Request, bucket string) {
	var in notificationConfiguration
	if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &in); err != nil {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	var rules []repository.NotificationRule
	for _, tc := range in.TopicConfigs {
		rules = append(rules, repository.NotificationRule{
			ID: tc.ID, Events: tc.Events, TopicARN: tc.TopicARN,
			FilterKey: filterKey(tc.Filter),
		})
	}
	for _, qc := range in.QueueConfigs {
		rules = append(rules, repository.NotificationRule{
			ID: qc.ID, Events: qc.Events, QueueARN: qc.QueueARN,
			FilterKey: filterKey(qc.Filter),
		})
	}
	for _, lc := range in.LambdaConfigs {
		rules = append(rules, repository.NotificationRule{
			ID: lc.ID, Events: lc.Events, LambdaARN: lc.LambdaARN,
			FilterKey: filterKey(lc.Filter),
		})
	}
	if err := h.svc.SetBucketNotifications(r.Context(), mw.TenantFrom(r.Context()), bucket, rules); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteBucketNotifications(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := h.svc.DeleteBucketNotifications(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func filterFromKey(key string) *filter {
	if key == "" {
		return nil
	}
	return &filter{S3Key: filterRule{Name: "prefix", Value: filterVal{Value: key}}}
}

func filterKey(f *filter) string {
	if f == nil {
		return ""
	}
	return f.S3Key.Value.Value
}

// ── Bucket accelerate (stub) ────────────────────────────────────────────────

type accelerateConfig struct {
	XMLNs  string `xml:"xmlns,attr"`
	Status string `xml:"Status"`
}

func (h *Handler) getBucketAccelerate(w http.ResponseWriter, r *http.Request, bucket string) {
	_ = bucket
	writeXML(w, http.StatusOK, accelerateConfig{
		XMLNs: s3Namespace, Status: "Suspended",
	})
}

// ── Restore Object ──────────────────────────────────────────────────────────

func (h *Handler) restoreObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	tenant := mw.TenantFrom(r.Context())
	if err := h.svc.RestoreObject(r.Context(), tenant, bucket, key); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, xml.Header+`<RestoreObjectResult xmlns="%s"><RestoreOutput><RestoreStatus>restored</RestoreStatus></RestoreOutput></RestoreObjectResult>`, s3Namespace)
}
