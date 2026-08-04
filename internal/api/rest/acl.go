package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// allowAnonymous gates anonymous (unauthenticated) object reads: an anonymous
// request is allowed only when the object is public-readable, otherwise it is
// rejected with 403. Authenticated requests always pass.
func (h *Handler) allowAnonymous(w http.ResponseWriter, r *http.Request, key string) bool {
	if !auth.IsAnonymous(r.Context()) {
		return true
	}
	if h.svc.ObjectPublicReadable(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key) {
		tenant := mw.TenantFrom(r.Context())
		capability := access.Capability{
			ID: "canned-public-read", TenantID: tenant,
			Bucket: service.DefaultBucket, Key: key,
			Actions: []access.Action{access.ActionRead, access.ActionPreview, access.ActionDownload},
		}
		ctx := access.WithPrincipal(r.Context(), access.CapabilityPrincipal(access.PrincipalPublic, capability))
		*r = *r.WithContext(ctx)
		return true
	}
	h.writeError(w, r, service.ErrForbidden)
	return false
}

func (h *Handler) rejectAnonymousSubresource(w http.ResponseWriter, r *http.Request) bool {
	if !auth.IsAnonymous(r.Context()) {
		return false
	}
	h.writeError(w, r, service.ErrForbidden)
	return true
}

func errInvalidJSON(err error) error {
	return fmt.Errorf("%w: invalid JSON: %v", service.ErrInvalidArgs, err)
}

type aclBody struct {
	ACL string `json:"acl"`
}

// GET /v1/files/<key>/acl
func (h *Handler) GetObjectACLHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(keyFromPath(r), "/acl")
	acl, err := h.svc.GetObjectACL(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, aclBody{ACL: acl})
}

// PUT /v1/files/<key>/acl   {"acl":"public-read"}
func (h *Handler) PutObjectACLHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(keyFromPath(r), "/acl")
	var body aclBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if err := h.svc.SetObjectACL(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, key, body.ACL); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, aclBody{ACL: body.ACL})
}

// GET /v1/buckets/{bucket}/acl
func (h *Handler) GetBucketACL(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	cfg, err := h.svc.GetBucketConfigAuthorized(r.Context(), mw.TenantFrom(r.Context()), bucket)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	acl := cfg.ACL
	if acl == "" {
		acl = service.ACLPrivate
	}
	writeJSON(w, http.StatusOK, aclBody{ACL: acl})
}

// PUT /v1/buckets/{bucket}/acl   {"acl":"public-read"}
func (h *Handler) PutBucketACL(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	var body aclBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, errInvalidJSON(err))
		return
	}
	if err := h.svc.SetBucketACL(r.Context(), mw.TenantFrom(r.Context()), bucket, body.ACL); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, aclBody{ACL: body.ACL})
}
