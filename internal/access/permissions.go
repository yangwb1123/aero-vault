package access

// Permission names are the provider-facing vocabulary (external contract);
// Actions are the internal PDP vocabulary. One mapping table (FR-3): the
// permission "vault.file.delete" is the single canonical name for object
// deletion, resolved to the PDP action ActionDelete.
const PermissionVaultFileDelete = "vault.file.delete"

// SystemActorAntivirus is the one internal system principal allowed to delete
// without an authorizer (AV quarantine). Any other system actor fails closed
// under the fail-closed delete gate — see IsSystemDeleteExempt.
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

// IsSystemDeleteExempt reports whether the principal is the one internal
// system actor allowed to delete while no authorizer is configured (the AV
// quarantine path). The match requires exact tenant equality (no "*"
// wildcard): the antivirus worker always pins obj.TenantID, so a wildcard
// would grant any system actor with a "*" tenant delete rights everywhere.
func IsSystemDeleteExempt(principal Principal, tenant string) bool {
	return principal.Kind == PrincipalSystem &&
		principal.SubjectID == SystemActorAntivirus &&
		principal.TenantID != "" && principal.TenantID == tenant
}
