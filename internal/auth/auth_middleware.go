package auth

import (
	"context"
	"net/http"
	"strings"
)

// Middleware authenticates each request, rejecting missing/invalid keys when
// the registry is enabled. When disabled, requests pass through unchanged.
//
// Authorization header format: "Bearer <token>" or "ApiKey <token>".
func (r *Registry) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
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

func isBypassPath(path string) bool {
	return path == "/healthz" || path == "/readyz" || path == "/metrics" ||
		path == "/openapi.json" || path == "/docs" ||
		strings.HasPrefix(path, "/ui")
}

func (r *Registry) authenticateSigV4(w http.ResponseWriter, req *http.Request) (*http.Request, bool) {
	k, err := r.sigv4.Verify(req)
	if err != nil {
		forbidden(w, err.Error())
		return nil, false
	}
	if k.Tenant != "*" {
		req.Header.Set("X-Aero-Tenant", k.Tenant)
	}
	if ok, required := checkScope(req.Method, k); !ok {
		forbidden(w, "missing scope: "+string(required))
		return nil, false
	}
	decodeStreamingBody(req)
	ctx := context.WithValue(req.Context(), ctxKeyKey, k)
	return req.WithContext(ctx), true
}

func (r *Registry) authenticateBearer(w http.ResponseWriter, req *http.Request) (*http.Request, bool) {
	token := extractToken(req)
	if token == "" {
		if r.anonRead && isObjectReadPath(req.Method, req.URL.Path) {
			ctx := context.WithValue(req.Context(), anonCtxKey, true)
			return req.WithContext(ctx), true
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
	ctx := context.WithValue(req.Context(), ctxKeyKey, k)
	return req.WithContext(ctx), true
}

func checkScope(method string, k Key) (bool, Scope) {
	required := ScopeWrite
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND", "PROPPATCH":
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
		for _, prefix := range []string{"Bearer ", "ApiKey ", "bearer ", "apikey "} {
			if strings.HasPrefix(h, prefix) {
				return strings.TrimSpace(h[len(prefix):])
			}
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
