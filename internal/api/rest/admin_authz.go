package rest

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/service"
)

// AuthorizationProvider is the admin-delete boundary port. Keeping it
// identical to access.Authorizer lets external PDP adapters be injected
// without coupling this protocol layer to access.Manager.
type AuthorizationProvider interface {
	Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error)
}

// adminDeleteAuthzTimeout bounds a provider call at the request boundary.
// Tests may shorten it; production keeps a finite fail-closed upper bound.
var adminDeleteAuthzTimeout = 10 * time.Second

// AdminMatrixProvider is the built-in default adapter for the administrative
// surface. Object ACLs, ownership, and write/read scopes are intentionally not
// consulted here: only an operator or tenant-admin role may govern deletion.
type AdminMatrixProvider struct{}

func (AdminMatrixProvider) Authorize(
	_ context.Context, principal access.Principal, action access.Action, resource access.Resource,
) (access.Decision, error) {
	if action != access.ActionDelete {
		return access.Decision{Allowed: false, Reason: "invalid_delete_action"}, nil
	}
	if principal.TenantID != "*" && principal.TenantID != resource.TenantID {
		return access.Decision{Allowed: false, Reason: "tenant_mismatch"}, nil
	}
	if access.IsAdministrator(principal) {
		return access.Decision{Allowed: true, Reason: "administrator"}, nil
	}
	return access.Decision{Allowed: false, Reason: "default_deny"}, nil
}

func (h *AdminHandler) authorizeFileDelete(
	w http.ResponseWriter, r *http.Request, tenant, bucket, key string,
) bool {
	if h.authz == nil {
		h.writeError(w, r, fmt.Errorf("%w: no authorization provider configured", service.ErrForbidden))
		return false
	}
	action, ok := access.ActionForPermission(access.PermissionVaultFileDelete)
	if !ok {
		h.writeError(w, r, fmt.Errorf("%w: delete permission is not registered", service.ErrForbidden))
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), adminDeleteAuthzTimeout)
	defer cancel()
	principal, _ := access.PrincipalFrom(ctx)
	decision, err := callAdminProvider(ctx, h.authz, principal, action, access.Resource{
		TenantID: tenant,
		Bucket:   bucket,
		Key:      key,
		Kind:     access.ResourceObject,
	})
	if err != nil {
		// Provider details are useful in server logs but must not become a
		// remotely observable authorization oracle.
		h.logAdminAuthzFailure(err, tenant, bucket, key)
		h.writeError(w, r, service.ErrForbidden)
		return false
	}
	if !decision.Allowed {
		h.writeError(w, r, fmt.Errorf("%w: %s", service.ErrForbidden, decision.Reason))
		return false
	}
	return true
}

func callAdminProvider(
	ctx context.Context, provider AuthorizationProvider, principal access.Principal,
	action access.Action, resource access.Resource,
) (decision access.Decision, err error) {
	if provider == nil {
		return access.Decision{}, fmt.Errorf("authorization provider is nil")
	}
	type result struct {
		decision access.Decision
		err      error
	}
	results := make(chan result, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				results <- result{err: fmt.Errorf("authorization provider panic")}
			}
		}()
		decision, err := provider.Authorize(ctx, principal, action, resource)
		results <- result{decision: decision, err: err}
	}()
	select {
	case out := <-results:
		return out.decision, out.err
	case <-ctx.Done():
		return access.Decision{}, ctx.Err()
	}
}

func (h *AdminHandler) logAdminAuthzFailure(err error, tenant, bucket, key string) {
	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("admin delete authorization provider failed",
		"tenant", tenant, "bucket", bucket, "key", key, "err", err)
}

var _ AuthorizationProvider = AdminMatrixProvider{}
