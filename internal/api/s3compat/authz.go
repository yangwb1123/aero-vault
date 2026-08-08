package s3compat

import (
	"context"

	"github.com/aero-vault/aero-vault/internal/access"
)

// AuthorizationProvider is the s3compat-boundary port for fail-closed
// enforcement of vault.file.delete (access.PermissionVaultFileDelete =
// access.ActionDelete) on S3 object deletion. The shape mirrors
// access.Authorizer so *access.Manager satisfies it structurally — same
// decision source as the FileService gate (REST parity), zero wrapper.
//
// A nil provider means "not configured" and MUST deny (fail-closed) — the
// deliberate inversion of the service-side nil-authorizer baseline
// (service/access.go:91-93), scoped to object deletion only (AC-7).
type AuthorizationProvider interface {
	Authorize(ctx context.Context, principal access.Principal,
		action access.Action, resource access.Resource) (access.Decision, error)
}

// authorizeDelete enforces the single/per-key object-delete gate. Any of
// provider-unset, provider-error, or non-allow decision denies. Provider
// errors are logged server-side (Warn) and never surfaced to the client;
// deny reasons are Debug-only (R2).
func (h *Handler) authorizeDelete(ctx context.Context, tenant, bucket, key string) bool {
	if h.authz == nil {
		return false
	}
	principal, _ := access.PrincipalFrom(ctx)
	decision, err := h.authz.Authorize(ctx, principal, access.ActionDelete,
		access.Resource{TenantID: tenant, Bucket: bucket, Key: key})
	if err != nil {
		h.logger.Warn("s3 delete authorization provider error; denying",
			"tenant", tenant, "bucket", bucket, "key", key, "err", err)
		return false
	}
	if !decision.Allowed {
		h.logger.Debug("s3 delete denied by provider",
			"tenant", tenant, "bucket", bucket, "key", key, "reason", decision.Reason)
		return false
	}
	return true
}
