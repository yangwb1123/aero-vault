package s3compat

import (
	"encoding/xml"
	"io"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
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
	if cfg.ExpireAfterDays <= 0 {
		writeS3Error(w, r, errNoSuchLifecycle)
		return
	}
	writeXML(w, http.StatusOK, lifecycleConfiguration{
		Xmlns: s3Namespace,
		Rules: []lifecycleRule{{
			Status:     "Enabled",
			Expiration: &lifecycleExpiration{Days: cfg.ExpireAfterDays},
		}},
	})
}

func (h *Handler) putBucketLifecycle(w http.ResponseWriter, r *http.Request, bucket string) {
	var in lifecycleConfiguration
	if err := decodeBucketBody(r, &in); err != nil {
		writeMalformedXML(w, r)
		return
	}
	days := 0
	for _, rule := range in.Rules {
		if rule.Expiration != nil && rule.Expiration.Days > 0 {
			days = rule.Expiration.Days
			break
		}
	}
	if err := h.svc.SetBucketLifecycle(r.Context(), mw.TenantFrom(r.Context()), bucket, days, "soft_delete"); err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
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
// the keys returned by a prefix listing. The repository tracks superseded
// versions by setting deleted_at on the prior row and has no distinct
// delete-marker entity, so every stored version row is reported as a <Version>;
// the newest (updated_at DESC, row index 0) carries IsLatest=true. Pagination of
// the per-key version enumeration is not exposed, so IsTruncated is always false
// and ?key-marker is echoed back without driving pagination.
func (h *Handler) listObjectVersions(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	keyMarker := q.Get("key-marker")
	tenant := mw.TenantFrom(r.Context())

	page, err := h.svc.List(r.Context(), tenant, bucket, prefix, "", 1000)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	out := listVersionsResult{
		Xmlns:       s3Namespace,
		Name:        bucket,
		Prefix:      prefix,
		KeyMarker:   keyMarker,
		MaxKeys:     1000,
		IsTruncated: false,
	}
	for _, k := range page.Objects {
		versions, err := h.svc.Repo().ListObjectVersions(r.Context(), tenant, bucket, k.Key)
		if err != nil {
			writeS3Error(w, r, err)
			return
		}
		for i, v := range versions {
			out.Versions = append(out.Versions, versionEntry{
				Key:          v.Key,
				VersionID:    v.VersionID,
				IsLatest:     i == 0,
				LastModified: v.UpdatedAt.UTC(),
				ETag:         `"` + v.ETag + `"`,
				Size:         v.Size,
				StorageClass: "STANDARD",
			})
		}
	}
	writeXML(w, http.StatusOK, out)
}

// writeMalformedXML reports a request body that could not be parsed as a 400
// MalformedXML, matching AWS.
func writeMalformedXML(w http.ResponseWriter, r *http.Request) {
	writeS3Error(w, r, errMalformedXML)
}
