package middleware

import (
	"context"
	"net/http"
	"strings"
)

// TenantStatusLookup returns the persisted status for a known tenant. Unknown
// tenants remain allowed for backward compatibility with implicit tenants.
type TenantStatusLookup func(context.Context, string) (status string, found bool, err error)

// TenantWithStatus preserves Tenant's context behavior and additionally rejects
// requests for known disabled tenants. It occupies the same Tenant chain slot.
func TenantWithStatus(lookup TenantStatusLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant := r.Header.Get(TenantHeader)
			if tenant == "" {
				tenant = "default"
			}
			ctx := context.WithValue(r.Context(), ctxTenantID, tenant)
			if lookup == nil || tenantStatusBypass(r.URL.Path) {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			status, found, err := lookup(ctx, tenant)
			if err != nil {
				writeTenantStatusError(w, http.StatusServiceUnavailable,
					"TenantStatusUnavailable", "tenant status unavailable")
				return
			}
			if found && status == "disabled" {
				writeTenantStatusError(w, http.StatusForbidden, "TenantDisabled", "tenant is disabled")
				return
			}
			ctx = context.WithValue(ctx, ctxTenantStatusVerified, tenant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeTenantStatusError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + message + `"}}` + "\n"))
}

func tenantStatusBypass(path string) bool {
	return path == "/" || path == "/favicon.ico" || path == "/healthz" ||
		path == "/readyz" || path == "/metrics" || path == "/openapi.json" ||
		path == "/docs" || strings.HasPrefix(path, "/ui") ||
		strings.HasPrefix(path, "/auth/oidc/") || strings.HasPrefix(path, "/share/") ||
		strings.HasPrefix(path, "/public/assets/")
}
