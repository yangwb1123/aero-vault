package access

// Permission names are the provider-facing vocabulary (external contract);
// Actions are the internal PDP vocabulary. One mapping table (FR-3): the
// permission "vault.file.delete" is the single canonical name for object
// deletion, resolved to the PDP action ActionDelete.
const PermissionVaultFileDelete = "vault.file.delete"

// SystemActorAntivirus identifies the actor used by the AV quarantine path.
// It remains part of the public context vocabulary for compatibility; all
// trusted system contexts are exempt from the delete gate below.
const SystemActorAntivirus = "system:antivirus"

// ActionForPermission resolves an external permission name to its PDP action.
// Unknown names return ok=false (no registry, no scope vocabulary change).
func ActionForPermission(permission string) (Action, bool) {
	switch permission {
	case PermissionVaultFileDelete:
		return ActionDelete, true
	}
	return "", false
}

// IsSystemDeleteExempt reports whether an internal system principal may delete
// while no authorizer is configured. The match requires exact tenant equality
// (no "*" wildcard), so a system context cannot cross tenant boundaries.
func IsSystemDeleteExempt(principal Principal, tenant string) bool {
	return principal.Kind == PrincipalSystem &&
		principal.TenantID != "" && principal.TenantID == tenant
}
