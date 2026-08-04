package access

import "context"

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func SystemContext(ctx context.Context, tenant string) context.Context {
	return WithPrincipal(ctx, Principal{
		SubjectID: "aero-vault-system",
		TenantID:  tenant,
		Kind:      PrincipalSystem,
	})
}

func CapabilityPrincipal(kind PrincipalKind, capability Capability) Principal {
	return Principal{
		SubjectID:  string(kind) + ":" + capability.ID,
		TenantID:   capability.TenantID,
		Kind:       kind,
		Capability: &capability,
	}
}
