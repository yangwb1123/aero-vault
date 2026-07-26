package s3compat

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// bucketConfigBodyLimit bounds the XML body of bucket sub-resource PUTs. These
// configs are tiny; anything larger is a malformed/abusive request.
const bucketConfigBodyLimit = 64 << 10

// decodeBucketBody reads at most bucketConfigBodyLimit bytes and unmarshals the
// XML into v. A parse error surfaces to the caller as a 400 MalformedXML.
func decodeBucketBody(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, bucketConfigBodyLimit))
	if err != nil {
		return err
	}
	return xml.Unmarshal(body, v)
}

// --- Versioning (GET/PUT ?versioning) ---------------------------------------

func (h *Handler) getBucketVersioning(w http.ResponseWriter, r *http.Request, bucket string) {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := versioningConfiguration{Xmlns: s3Namespace}
	if cfg.Versioning {
		out.Status = "Enabled"
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) putBucketVersioning(w http.ResponseWriter, r *http.Request, bucket string) {
	var in versioningConfiguration
	if err := decodeBucketBody(r, &in); err != nil {
		writeMalformedXML(w, r)
		return
	}
	if err := h.svc.SetBucketVersioning(r.Context(), mw.TenantFrom(r.Context()), bucket, in.Status == "Enabled"); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Lifecycle (GET/PUT ?lifecycle) -----------------------------------------

func (h *Handler) getBucketLifecycle(w http.ResponseWriter, r *http.Request, bucket string) {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if cfg.ExpireAfterDays <= 0 && len(cfg.TransitionRules) == 0 {
		writeS3Error(w, r, errNoSuchLifecycle)
		return
	}
	// Build one rule per configuration type.
	rules := []lifecycleRule{}
	if cfg.ExpireAfterDays > 0 {
		rules = append(rules, lifecycleRule{
			ID:         "expire-all",
			Status:     "Enabled",
			Expiration: &lifecycleExpiration{Days: cfg.ExpireAfterDays},
		})
	}
	for i, tr := range cfg.TransitionRules {
		rule := lifecycleRule{
			ID:     fmt.Sprintf("transition-%d", i+1),
			Status: "Enabled",
			Transition: &lifecycleTransition{
				Days:         tr.Days,
				StorageClass: tr.StorageClass,
			},
		}
		rules = append(rules, rule)
	}
	if cfg.NoncurrentDays > 0 {
		rules = append(rules, lifecycleRule{
			ID:     "noncurrent-expire",
			Status: "Enabled",
			NoncurrentVersionExpiration: &lifecycleNoncurrentExp{
				NoncurrentDays: cfg.NoncurrentDays,
			},
		})
	}
	if cfg.NoncurrentTransitionDays > 0 && cfg.NoncurrentTransitionStorageClass != "" {
		rules = append(rules, lifecycleRule{
			ID:     "noncurrent-transition",
			Status: "Enabled",
			NoncurrentVersionTransition: &lifecycleNoncurrentTrans{
				NoncurrentDays: cfg.NoncurrentTransitionDays,
				StorageClass:   cfg.NoncurrentTransitionStorageClass,
			},
		})
	}
	writeXML(w, http.StatusOK, lifecycleConfiguration{Xmlns: s3Namespace, Rules: rules})
}

func (h *Handler) putBucketLifecycle(w http.ResponseWriter, r *http.Request, bucket string) {
	var in lifecycleConfiguration
	if err := decodeBucketBody(r, &in); err != nil {
		writeMalformedXML(w, r)
		return
	}
	lc := repository.LifecycleConfig{}
	for _, rule := range in.Rules {
		if rule.Status == "Disabled" {
			continue
		}
		if rule.Expiration != nil && rule.Expiration.Days > 0 {
			lc.ExpireAfterDays = rule.Expiration.Days
			lc.ExpireAction = "soft_delete"
		}
		if rule.Transition != nil && rule.Transition.Days > 0 && rule.Transition.StorageClass != "" {
			lc.TransitionRules = append(lc.TransitionRules, repository.TransitionRule{
				Days:         rule.Transition.Days,
				StorageClass: rule.Transition.StorageClass,
			})
		}
		if rule.NoncurrentVersionExpiration != nil && rule.NoncurrentVersionExpiration.NoncurrentDays > 0 {
			lc.NoncurrentDays = rule.NoncurrentVersionExpiration.NoncurrentDays
		}
		if rule.NoncurrentVersionTransition != nil && rule.NoncurrentVersionTransition.NoncurrentDays > 0 && rule.NoncurrentVersionTransition.StorageClass != "" {
			lc.NoncurrentTransitionDays = rule.NoncurrentVersionTransition.NoncurrentDays
			lc.NoncurrentTransitionStorageClass = rule.NoncurrentVersionTransition.StorageClass
		}
	}
	if err := h.svc.SetBucketLifecycleFull(r.Context(), mw.TenantFrom(r.Context()), bucket, lc); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// deleteBucketLifecycle clears the bucket's expiry policy (days=0 disables it),
// matching AWS DeleteBucketLifecycle which returns 204 No Content.
func (h *Handler) deleteBucketLifecycle(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := h.svc.SetBucketLifecycle(r.Context(), mw.TenantFrom(r.Context()), bucket, 0, "soft_delete"); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Bucket ACL (GET/PUT ?acl) ----------------------------------------------

func (h *Handler) getBucketACL(w http.ResponseWriter, r *http.Request, bucket string) {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	acl := cfg.ACL
	if acl == "" {
		acl = "private"
	}
	writeXML(w, http.StatusOK, cannedToPolicy(acl))
}

func (h *Handler) putBucketACL(w http.ResponseWriter, r *http.Request, bucket string) {
	// Prefer the canned ACL on the x-amz-acl header (the common form); otherwise
	// map an AccessControlPolicy body back to the nearest canned value. The body
	// is read bounded once; an absent/empty body (no header) defaults to private,
	// independent of Content-Length vs chunked transfer encoding.
	acl := r.Header.Get("x-amz-acl")
	if acl == "" {
		body, err := io.ReadAll(io.LimitReader(r.Body, bucketConfigBodyLimit))
		if err != nil {
			writeS3Error(w, r, err)
			return
		}
		if len(bytes.TrimSpace(body)) > 0 {
			var in accessControlPolicy
			if err := xml.Unmarshal(body, &in); err != nil {
				writeMalformedXML(w, r)
				return
			}
			acl = policyToCanned(in)
		}
	}
	if acl == "" {
		acl = "private"
	}
	if err := h.svc.SetBucketACL(r.Context(), mw.TenantFrom(r.Context()), bucket, acl); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Bucket location (GET ?location) ----------------------------------------

func (h *Handler) getBucketLocation(w http.ResponseWriter, r *http.Request, bucket string) {
	exists, err := h.svc.HeadBucket(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	if !exists {
		writeS3Error(w, r, errNoSuchBucket)
		return
	}
	// Empty constraint = us-east-1, the standard single-region response.
	writeXML(w, http.StatusOK, locationConstraint{Xmlns: s3Namespace})
}

// --- Object lock (GET/PUT ?object-lock) -------------------------------------

func (h *Handler) getBucketObjectLock(w http.ResponseWriter, r *http.Request, bucket string) {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := objectLockConfiguration{Xmlns: s3Namespace, ObjectLockEnabled: "Enabled"}
	if cfg.ObjectLockSeconds > 0 {
		days := cfg.ObjectLockSeconds / 86400
		if days < 1 {
			days = 1
		}
		out.Rule = &objectLockRule{DefaultRetention: objectLockRetention{Mode: "GOVERNANCE", Days: days}}
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) putBucketObjectLock(w http.ResponseWriter, r *http.Request, bucket string) {
	var in objectLockConfiguration
	if err := decodeBucketBody(r, &in); err != nil {
		writeMalformedXML(w, r)
		return
	}
	seconds := 0
	if in.Rule != nil {
		seconds = in.Rule.DefaultRetention.Days * 86400
	}
	if err := h.svc.SetBucketObjectLock(r.Context(), mw.TenantFrom(r.Context()), bucket, seconds); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- ListObjectVersions (GET ?versions) -------------------------------------

// listObjectVersions composes the per-key ListObjectVersions repository call over
// one bounded page of keys. The repository tracks superseded versions by setting
// deleted_at on the prior row and has no distinct delete-marker entity, so every
// stored version row is reported as a <Version>; the newest (updated_at DESC, row
// index 0) carries IsLatest=true.
//
// Pagination is by KEY: ?max-keys (default/cap 1000) bounds the keys scanned per
// request and ?key-marker continues from a prior page. When more keys remain,
// IsTruncated=true and <NextKeyMarker> carries the continuation key — so buckets
// with more than one page of keys are enumerated fully across requests rather
// than silently truncated.
//
// Deep pagination within a single key's version list is supported via
// ?version-id-marker. When set, versions are enumerated starting after that ID.
func (h *Handler) listObjectVersions(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	keyMarker := q.Get("key-marker")
	versionIDMarker := q.Get("version-id-marker")
	tenant := mw.TenantFrom(r.Context())

	maxKeys, _ := strconv.Atoi(q.Get("max-keys"))
	if maxKeys <= 0 || maxKeys > 1000 {
		maxKeys = 1000
	}

	page, err := h.svc.List(r.Context(), tenant, bucket, prefix, keyMarker, maxKeys)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listVersionsResult{
		Xmlns:           s3Namespace,
		Name:            bucket,
		Prefix:          prefix,
		KeyMarker:       keyMarker,
		VersionIdMarker: versionIDMarker,
		MaxKeys:         maxKeys,
		IsTruncated:     page.HasMore,
	}
	if page.HasMore {
		out.NextKeyMarker = page.NextMarker
	}
	for _, k := range page.Objects {
		vopts := repository.VersionListOpts{}
		// Only pass version-id-marker for the first/matching key.
		if versionIDMarker != "" && out.NextVersionIdMarker == "" {
			vopts.VersionIDMarker = versionIDMarker
			versionIDMarker = "" // consumed for this key; subsequent keys iterate from start
		}
		vpage, err := h.svc.Repo().ListObjectVersionsWithOpts(r.Context(), tenant, bucket, k.Key, vopts)
		if err != nil {
			writeS3Error(w, r, err)
			return
		}
		for i, v := range vpage.Versions {
			out.Versions = append(out.Versions, versionEntry{
				Key:          v.Key,
				VersionID:    v.VersionID,
				IsLatest:     i == 0,
				LastModified: v.UpdatedAt.UTC(),
				ETag:         `"` + v.ETag + `"`,
				Size:         v.Size,
				StorageClass: service.StorageClassOrDefault(v.StorageClass),
			})
		}
		if vpage.HasMore {
			out.IsTruncated = true
			out.NextVersionIdMarker = vpage.NextVersionID
			out.NextKeyMarker = k.Key
			break
		}
	}
	writeXML(w, http.StatusOK, out)
}

// writeMalformedXML reports a request body that could not be parsed as a 400
// MalformedXML, matching AWS.
func writeMalformedXML(w http.ResponseWriter, r *http.Request) {
	writeS3Error(w, r, errMalformedXML)
}

// --- Website (GET/PUT/DELETE ?website) ---------------------------------------

func (h *Handler) getBucketWebsite(w http.ResponseWriter, r *http.Request, bucket string) {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	wc := cfg.WebsiteConfig
	if wc.IndexDocument.Suffix == "" && wc.ErrorDocument.Key == "" {
		writeS3Error(w, r, errNoSuchWebsite)
		return
	}
	out := websiteConfiguration{
		Xmlns: s3Namespace,
	}
	if wc.IndexDocument.Suffix != "" {
		out.IndexDocument = &websiteIndexDoc{Suffix: wc.IndexDocument.Suffix}
	}
	if wc.ErrorDocument.Key != "" {
		out.ErrorDocument = &websiteErrorDoc{Key: wc.ErrorDocument.Key}
	}
	writeXML(w, http.StatusOK, out)
}

func (h *Handler) putBucketWebsite(w http.ResponseWriter, r *http.Request, bucket string) {
	var in websiteConfiguration
	if err := decodeBucketBody(r, &in); err != nil {
		writeMalformedXML(w, r)
		return
	}
	wc := repository.WebsiteConfig{}
	if in.IndexDocument != nil {
		wc.IndexDocument.Suffix = in.IndexDocument.Suffix
	}
	if in.ErrorDocument != nil {
		wc.ErrorDocument.Key = in.ErrorDocument.Key
	}
	if err := h.svc.SetBucketWebsite(r.Context(), mw.TenantFrom(r.Context()), bucket, wc); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteBucketWebsite(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := h.svc.DeleteBucketWebsite(r.Context(), mw.TenantFrom(r.Context()), bucket); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
