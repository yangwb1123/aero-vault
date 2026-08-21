package rest

import (
	"fmt"
	"net/http"

	"github.com/aero-vault/aero-vault/internal/auth"
)

// requireAdminForTenant permits an admin key to operate on its own tenant or
// permits an operator key (tenant="*") to operate on any tenant.
func (h *AdminHandler) requireAdminForTenant(w http.ResponseWriter, r *http.Request, tenant string) bool {
	if !h.requireAdmin(w, r) {
		return false
	}
	return h.requireTenantBoundary(w, r, tenant)
}

// requireTenantBoundary is used after a handler has decoded a target tenant.
// No-auth mode retains the existing implicit-operator behavior.
func (h *AdminHandler) requireTenantBoundary(w http.ResponseWriter, r *http.Request, tenant string) bool {
	if h.reg == nil || !h.reg.Enabled() {
		return true
	}
	k, ok := auth.FromContext(r.Context())
	if ok && (k.Tenant == "*" || k.Tenant == tenant) {
		return true
	}
	writeAdminForbidden(w, fmt.Sprintf("tenant_mismatch: admin key is scoped to %q", k.Tenant))
	return false
}

// requireOperatorAdmin protects views and mutations that aggregate or control
// more than one tenant. A tenant admin must not use these endpoints as an
// indirect cross-tenant read/write primitive.
func (h *AdminHandler) requireOperatorAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !h.requireAdmin(w, r) {
		return false
	}
	if h.reg == nil || !h.reg.Enabled() {
		return true
	}
	k, ok := auth.FromContext(r.Context())
	if ok && k.Tenant == "*" {
		return true
	}
	writeAdminForbidden(w, "operator admin scope required")
	return false
}

func writeAdminForbidden(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusForbidden, errorBody{Error: errorPayload{
		Code: "Forbidden", Message: message,
	}})
}

func filterAdminKeys(r *http.Request, keys []auth.Key) []auth.Key {
	k, ok := auth.FromContext(r.Context())
	if !ok || k.Tenant == "*" {
		return keys
	}
	out := make([]auth.Key, 0, len(keys))
	for _, item := range keys {
		if item.Tenant == k.Tenant {
			out = append(out, item)
		}
	}
	return out
}

// redactToken masks a raw API-key token for audit-log storage, keeping a short
// suffix so an operator can correlate it without persisting the secret.
func redactToken(tok string) string {
	if len(tok) <= 4 {
		return "****"
	}
	return "****" + tok[len(tok)-4:]
}

// requireAdmin gates admin routes when auth is enabled. Without auth, the
// caller is implicitly admin (mirrors the no-auth MVP behaviour).
func (h *AdminHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.reg == nil || !h.reg.Enabled() {
		return true
	}
	k, ok := auth.FromContext(r.Context())
	if !ok || !k.Has(auth.ScopeAdmin) {
		writeJSON(w, http.StatusForbidden, errorBody{Error: errorPayload{
			Code: "Forbidden", Message: "admin scope required",
		}})
		return false
	}
	return true
}

// GetConfig returns a read-only snapshot of the server configuration,
// excluding sensitive fields (keys, secrets, tokens).
func (h *AdminHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if !h.requireOperatorAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": 1})
}

func writeAdminUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errorPayload{
		Code: "Unavailable", Message: "authentication registry is not configured",
	}})
}
