package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// IdempotencyReplayBoundary is mandatory on a cache hit. Tests of the cache
// transport may supply a real-reader fixture; production routers always use
// the Handler resolver, never an Allow callback or a retry-body resolver.
type IdempotencyReplayBoundary interface {
	AdmitReplay(*http.Request, repository.IdempotencyRecord) error
}

type replayTarget struct {
	key     string
	kind    string
	actions []access.Action
	policy  string
}

func (h *IdempotencyHandler) admitReplay(r *http.Request, rec repository.IdempotencyRecord) error {
	if adminPortMissing(h.boundary) {
		return replayFault(errors.New("missing idempotency replay boundary"))
	}
	if rec.TenantID != mw.TenantFrom(r.Context()) || rec.Key != r.Header.Get("Idempotency-Key") {
		return replayFault(errors.New("idempotency record scope conflict"))
	}
	if err := h.boundary.AdmitReplay(r, rec); err != nil {
		return err
	}
	if err := r.Context().Err(); err != nil {
		return replayFault(err)
	}
	return nil
}

func replayFault(cause error) error {
	return fmt.Errorf("%w: %w", access.ErrResourceBoundaryFault, cause)
}

func (h *Handler) AdmitReplay(r *http.Request, rec repository.IdempotencyRecord) error {
	if h == nil || h.svc == nil {
		return replayFault(errors.New("missing replay service"))
	}
	if rec.TenantID != mw.TenantFrom(r.Context()) || rec.Key != r.Header.Get("Idempotency-Key") {
		return replayFault(errors.New("cached response record scope conflict"))
	}
	target, err := resolveReplayTarget(r)
	if err != nil {
		return err
	}
	admission, err := cachedReplayAdmission(rec)
	if err != nil {
		return err
	}
	if err := validateCachedTarget(r, rec, target, admission); err != nil {
		return err
	}
	if err := h.bucketPolicyAdmission(r, admission.Resource.Key, target.policy); err != nil {
		return err
	}
	if err := h.validateReplayCanonical(r, admission); err != nil {
		return err
	}
	if target.kind == "presign" {
		if err := h.validateReplayPresign(r, rec, admission.Resource.Key); err != nil {
			return err
		}
	}
	return h.svc.AuthorizeReplayAdmission(r.Context(), admission, service.ReadOptions{})
}

// resolveReplayTarget mirrors postKey/putKey/patchKey/deleteKey. Suffixes are
// interpreted only for the methods which actually dispatch them that way.
func resolveReplayTarget(r *http.Request) (replayTarget, error) {
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	if path == "/files" && r.Method == http.MethodPost {
		return replayTarget{kind: "form", actions: putReplayActions(), policy: "s3:PutObject"}, nil
	}
	if !strings.HasPrefix(path, "/files/") {
		return replayTarget{}, replayFault(errors.New("unregistered replay route"))
	}
	key := strings.TrimPrefix(strings.TrimPrefix(path, "/files/"), "/")
	switch r.Method {
	case http.MethodPut:
		return resolvePutReplay(key), nil
	case http.MethodPost:
		return resolvePostReplay(r, key)
	case http.MethodPatch:
		if strings.HasSuffix(key, "/metadata") {
			return writeReplayTarget(key, "/metadata", "metadata"), nil
		}
	case http.MethodDelete:
		return resolveDeleteReplay(key), nil
	}
	return replayTarget{}, replayFault(errors.New("unsupported replay method or suffix"))
}

func resolveDeleteReplay(key string) replayTarget {
	if strings.HasSuffix(key, "/tags") {
		return writeReplayTarget(key, "/tags", "empty")
	}
	if strings.HasSuffix(key, "/metadata") {
		return writeReplayTarget(key, "/metadata", "metadata")
	}
	return replayTarget{key: key, kind: "empty", actions: []access.Action{access.ActionDelete}, policy: "s3:DeleteObject"}
}

func resolvePutReplay(key string) replayTarget {
	for _, suffix := range []string{"/tags", "/acl", "/metadata"} {
		if strings.HasSuffix(key, suffix) {
			target := writeReplayTarget(key, suffix, strings.TrimPrefix(suffix, "/"))
			if suffix == "/acl" {
				target.actions = []access.Action{access.ActionManageACL}
			}
			return target
		}
	}
	return replayTarget{key: key, kind: "object", actions: putReplayActions(), policy: "s3:PutObject"}
}

func resolvePostReplay(r *http.Request, key string) (replayTarget, error) {
	switch {
	case strings.HasSuffix(key, "/lock"):
		return writeReplayTarget(key, "/lock", "lock"), nil
	case strings.HasSuffix(key, "/restore"):
		t := writeReplayTarget(key, "/restore", "restore")
		t.actions = []access.Action{access.ActionRestore}
		return t, nil
	case strings.HasSuffix(key, "/presign"):
		t := writeReplayTarget(key, "/presign", "presign")
		switch r.URL.Query().Get("op") {
		case "", "get":
			t.actions, t.policy = []access.Action{access.ActionDownload}, "s3:GetObject"
		case "put":
			t.actions, t.policy = putReplayActions(), "s3:PutObject"
		default:
			return replayTarget{}, replayFault(errors.New("invalid presign replay operation"))
		}
		return t, nil
	default:
		return replayTarget{}, replayFault(errors.New("unsupported POST replay suffix"))
	}
}

func putReplayActions() []access.Action {
	return []access.Action{access.ActionCreate, access.ActionWrite}
}

func writeReplayTarget(key, suffix, kind string) replayTarget {
	return replayTarget{key: strings.TrimSuffix(key, suffix), kind: kind,
		actions: []access.Action{access.ActionWrite}, policy: ""}
}

// Only handlers which check an S3-style policy on fresh execution do so here;
// all routes independently recheck FileService authorization and admission.
func (h *Handler) bucketPolicyAdmission(r *http.Request, key, action string) error {
	tenant := mw.TenantFrom(r.Context())
	readers := DiscoverRouterResourceReaders(h.svc.Repo())
	if h.resourceReaders != nil {
		readers = *h.resourceReaders
	}
	boundary := access.NewResourceBoundary(readers.Binding, readers.Coverage)
	if err := boundary.CheckBucket(r.Context(), tenant, service.DefaultBucket); err != nil {
		return err
	}
	if action == "" {
		return nil
	}
	cfg, err := h.svc.GetBucketConfig(r.Context(), tenant, service.DefaultBucket)
	if err != nil {
		return replayFault(err)
	}
	if cfg.Policy == "" {
		return nil
	}
	p, err := auth.ParsePolicy(cfg.Policy)
	if err != nil || p == nil {
		return service.ErrForbidden
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !auth.AllowedResource(p, action, bucketPolicyResourceARN(key), host) {
		return service.ErrForbidden
	}
	return nil
}

func (h *Handler) validateReplayPresign(r *http.Request, rec repository.IdempotencyRecord, key string) error {
	if h.putPresigner == nil {
		return replayFault(errors.New("missing replay capability verifier"))
	}
	var dto presignResponse
	if err := json.Unmarshal(rec.ResponseBody, &dto); err != nil {
		return replayFault(err)
	}
	u, err := url.Parse(dto.URL)
	if err != nil {
		return replayFault(err)
	}
	if err := h.validateReplayPresignOrigin(r, u, key); err != nil {
		return err
	}
	request := &http.Request{Method: http.MethodGet, URL: u}
	if r.URL.Query().Get("op") == "put" {
		request.Method = http.MethodPut
		_, err = h.putPresigner.VerifyPut(request)
	} else {
		_, err = h.putPresigner.VerifyGet(request)
	}
	if err != nil {
		return replayFault(err)
	}
	return nil
}

func (h *Handler) validateReplayPresignOrigin(r *http.Request, got *url.URL, key string) error {
	target, err := h.restObjectTarget(r, key)
	if err != nil {
		return replayFault(err)
	}
	want, err := url.Parse(target)
	if err != nil {
		return replayFault(err)
	}
	if got.Scheme != want.Scheme || got.Host != want.Host || got.EscapedPath() != want.EscapedPath() {
		return replayFault(errors.New("cached presign origin conflict"))
	}
	return nil
}

func sameReplayObjectIdentity(got, want repository.Object) bool {
	return got.ID == want.ID && got.TenantID == want.TenantID && got.Bucket == want.Bucket &&
		got.Key == want.Key && got.VersionID == want.VersionID && service.ObjectOwner(got) == service.ObjectOwner(want)
}

func (h *Handler) validateReplayCanonical(r *http.Request, admission service.ReplayAdmission) error {
	if admission.Object == nil || admission.Action == access.ActionDelete {
		return nil // deleted rows may be gone; the original provider is still required
	}
	readers := DiscoverRouterResourceReaders(h.svc.Repo())
	if h.resourceReaders != nil {
		readers = *h.resourceReaders
	}
	if adminPortMissing(readers.Resources) {
		return replayFault(errors.New("missing exact replay resource reader"))
	}
	want := *admission.Object
	got, err := readers.Resources.GetObjectByID(r.Context(), want.ID)
	if err = errors.Join(err, r.Context().Err()); err != nil {
		return replayFault(err)
	}
	if !sameReplayObjectIdentity(got, want) {
		return replayFault(errors.New("cached object identity no longer matches"))
	}
	if got.DeletedAt != nil || service.IsDeleteMarker(got) {
		return service.ErrNotFound
	}
	if got.Metadata["_aero_scrub_status"] == "corrupt" {
		return service.ErrObjectCorrupt
	}
	return nil
}
