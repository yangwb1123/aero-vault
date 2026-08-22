package rest

import (
	"net/http"
	"slices"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
)

type sessionResponse struct {
	Authenticated bool                 `json:"authenticated"`
	SubjectID     string               `json:"subject_id,omitempty"`
	TenantID      string               `json:"tenant_id"`
	PrincipalKind access.PrincipalKind `json:"principal_kind"`
	Roles         []string             `json:"roles"`
	Groups        []string             `json:"groups"`
	Scopes        []string             `json:"scopes"`
}

// Session returns the normalized caller identity consumed by Aero Vault.
// Browser clients must use this contract instead of decoding Snaplink tokens.
func (h *Handler) Session(w http.ResponseWriter, r *http.Request) {
	principal, ok := access.PrincipalFrom(r.Context())
	tenant := mw.TenantFrom(r.Context())
	if _, present := mw.TenantFromContext(r.Context()); !present && principal.TenantID != "" {
		tenant = principal.TenantID
	}
	if !ok {
		principal.Kind = access.PrincipalAnonymous
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization, X-Api-Key, X-Aero-Tenant")
	writeJSON(w, http.StatusOK, sessionResponse{
		Authenticated: ok && principal.Kind != access.PrincipalAnonymous,
		SubjectID:     principal.SubjectID,
		TenantID:      tenant,
		PrincipalKind: principal.Kind,
		Roles:         sortedUnique(principal.Roles),
		Groups:        sortedUnique(principal.Groups),
		Scopes:        sortedUnique(principal.Scopes),
	})
}

func sortedUnique(values []string) []string {
	out := append([]string(nil), values...)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		return []string{}
	}
	return out
}
