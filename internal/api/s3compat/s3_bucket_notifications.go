package s3compat

import (
	"encoding/xml"
	"fmt"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

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

// ── Bucket accelerate ───────────────────────────────────────────────────────

type accelerateConfig struct {
	XMLNs  string `xml:"xmlns,attr"`
	Status string `xml:"Status"`
}

func (h *Handler) getBucketAccelerate(w http.ResponseWriter, r *http.Request, bucket string) {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	status := cfg.AccelerateStatus
	if status == "" {
		status = "Suspended"
	}
	writeXML(w, http.StatusOK, accelerateConfig{
		XMLNs: s3Namespace, Status: status,
	})
}

func (h *Handler) putBucketAccelerate(w http.ResponseWriter, r *http.Request, bucket string) {
	var config accelerateConfig
	if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &config); err != nil {
		writeS3Error(w, r, errMalformedXML)
		return
	}
	if err := h.svc.SetBucketAccelerate(
		r.Context(), mw.TenantFrom(r.Context()), bucket, config.Status,
	); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) dispatchBucketAccelerate(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method == http.MethodPut {
		h.putBucketAccelerate(w, r, bucket)
		return
	}
	h.getBucketAccelerate(w, r, bucket)
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
