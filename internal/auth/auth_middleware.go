package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/aero-vault/aero-vault/internal/access"
)

// Middleware authenticates each request, rejecting missing/invalid keys when
// the registry is enabled. When disabled, requests pass through unchanged.
//
// Authorization header format: "Bearer <token>" or "ApiKey <token>".
func (r *Registry) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			signer := r.PutPresigner()
			if signer != nil {
				if IsPresignedPut(req) {
					if req2, ok := r.authenticatePresignedPut(w, req, signer); ok {
						next.ServeHTTP(w, req2)
					}
					return
				}
				if IsPresignedGet(req) {
					if req2, ok := r.authenticatePresignedGet(w, req, signer); ok {
						next.ServeHTTP(w, req2)
					}
					return
				}
			}
			if !r.Enabled() {
				next.ServeHTTP(w, req)
				return
			}
			if isBypassPath(req.URL.Path) {
				next.ServeHTTP(w, req)
				return
			}
			if r.sigv4 != nil && IsSigned(req) {
				if req2, ok := r.authenticateSigV4(w, req); ok {
					next.ServeHTTP(w, req2)
				}
				return
			}
			if req2, ok := r.authenticateBearer(w, req); ok {
				next.ServeHTTP(w, req2)
			}
		})
	}
}

func (r *Registry) authenticatePresignedPut(
	w http.ResponseWriter,
	req *http.Request,
	signer *PutPresigner,
) (*http.Request, bool) {
	k, err := signer.VerifyPut(req)
	if err != nil {
		forbidden(w, err.Error())
		return nil, false
	}
	if hdr := req.Header.Get("X-Aero-Tenant"); hdr != "" && hdr != k.Tenant {
		forbidden(w, "tenant mismatch")
		return nil, false
	}
	req.Header.Set("X-Aero-Tenant", k.Tenant)
	ctx := contextWithKey(req.Context(), k)
	return req.WithContext(ctx), true
}

func (r *Registry) authenticatePresignedGet(
	w http.ResponseWriter,
	req *http.Request,
	signer *PutPresigner,
) (*http.Request, bool) {
	k, err := signer.VerifyGet(req)
	if err != nil {
		forbidden(w, err.Error())
		return nil, false
	}
	if hdr := req.Header.Get("X-Aero-Tenant"); hdr != "" && hdr != k.Tenant {
		forbidden(w, "tenant mismatch")
		return nil, false
	}
	const prefix = "/v1/files/"
	if !strings.HasPrefix(req.URL.Path, prefix) || len(req.URL.Path) == len(prefix) {
		forbidden(w, "presigned GET target must be a REST object")
		return nil, false
	}
	req.Header.Set("X-Aero-Tenant", k.Tenant)
	ctx := contextWithKey(req.Context(), k)
	capability := access.Capability{
		ID: "presigned-get", TenantID: k.Tenant, Bucket: "default",
		Key: strings.TrimPrefix(req.URL.Path, prefix),
		Actions: []access.Action{
			access.ActionRead, access.ActionPreview, access.ActionDownload,
		},
	}
	ctx = access.WithPrincipal(ctx, access.CapabilityPrincipal(access.PrincipalService, capability))
	return req.WithContext(ctx), true
}

func isBypassPath(path string) bool {
	return path == "/" || path == "/favicon.ico" ||
		path == "/healthz" || path == "/readyz" || path == "/metrics" ||
		path == "/openapi.json" || path == "/docs" ||
		strings.HasPrefix(path, "/auth/oidc/") ||
		strings.HasPrefix(path, "/ui")
}

func (r *Registry) authenticateSigV4(w http.ResponseWriter, req *http.Request) (*http.Request, bool) {
	k, err := r.sigv4.Verify(req)
	if err != nil {
		forbidden(w, err.Error())
		return nil, false
	}
	if k.Tenant != "*" {
		if hdr := req.Header.Get("X-Aero-Tenant"); hdr != "" && hdr != k.Tenant {
			forbidden(w, "tenant mismatch")
			return nil, false
		}
		req.Header.Set("X-Aero-Tenant", k.Tenant)
	}
	if ok, required := checkScope(req.Method, k); !ok {
		forbidden(w, "missing scope: "+string(required))
		return nil, false
	}
	if err := r.sigv4.PrepareBody(req); err != nil {
		forbidden(w, err.Error())
		return nil, false
	}
	ctx := contextWithKey(req.Context(), k)
	return req.WithContext(ctx), true
}

func (r *Registry) authenticateBearer(w http.ResponseWriter, req *http.Request) (*http.Request, bool) {
	token := extractToken(req)
	if token == "" {
		if isPublicCapabilityPath(req.Method, req.URL.Path) {
			return withAnonymousPrincipal(req), true
		}
		if r.anonRead && isObjectReadPath(req.Method, req.URL.Path) {
			return withAnonymousPrincipal(req), true
		}
		unauthorized(w, "missing Authorization header")
		return nil, false
	}
	k, ok := r.Lookup(req.Context(), token)
	if !ok {
		unauthorized(w, "invalid API key")
		return nil, false
	}
	if k.Tenant != "*" {
		if hdr := req.Header.Get("X-Aero-Tenant"); hdr != "" && hdr != k.Tenant {
			forbidden(w, "tenant mismatch")
			return nil, false
		}
		req.Header.Set("X-Aero-Tenant", k.Tenant)
	}
	if ok, required := checkScope(req.Method, k); !ok {
		forbidden(w, "missing scope: "+string(required))
		return nil, false
	}
	ctx := contextWithKey(req.Context(), k)
	return req.WithContext(ctx), true
}

func isPublicCapabilityPath(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	return strings.HasPrefix(path, "/share/") || strings.HasPrefix(path, "/public/assets/")
}

func withAnonymousPrincipal(req *http.Request) *http.Request {
	tenant := req.Header.Get("X-Aero-Tenant")
	if tenant == "" {
		tenant = "default"
	}
	ctx := context.WithValue(req.Context(), anonCtxKey, true)
	ctx = access.WithPrincipal(ctx, access.Principal{
		SubjectID: "anonymous", TenantID: tenant, Kind: access.PrincipalAnonymous,
	})
	return req.WithContext(ctx)
}

func checkScope(method string, k Key) (bool, Scope) {
	required := ScopeWrite
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND":
		required = ScopeRead
	}
	return k.Has(required), required
}

func (r *Registry) Require(s Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if !r.Enabled() {
				next.ServeHTTP(w, req)
				return
			}
			k, ok := FromContext(req.Context())
			if !ok {
				unauthorized(w, "not authenticated")
				return
			}
			if !k.Has(s) {
				forbidden(w, "missing scope: "+string(s))
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		fields := strings.Fields(h)
		if len(fields) == 2 &&
			(strings.EqualFold(fields[0], "Bearer") || strings.EqualFold(fields[0], "ApiKey")) {
			return fields[1]
		}
	}
	if h := r.Header.Get("X-Api-Key"); h != "" {
		return h
	}
	return ""
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="aero-vault"`)
	http.Error(w, msg, http.StatusUnauthorized)
}

func forbidden(w http.ResponseWriter, msg string) {
	http.Error(w, msg, http.StatusForbidden)
}
