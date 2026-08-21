package access

import "testing"

// T-8 (AC-4): the permission mapping table is the single source of truth for
// vault.file.delete ↔ ActionDelete. A regression in the map (renamed
// permission, wrong action, missing entry) fails here first.
func TestActionForPermission(t *testing.T) {
	action, ok := ActionForPermission(PermissionVaultFileDelete)
	if !ok {
		t.Fatalf("ActionForPermission(%q) ok=false; want true", PermissionVaultFileDelete)
	}
	if action != ActionDelete {
		t.Fatalf("ActionForPermission(%q) = %q; want %q", PermissionVaultFileDelete, action, ActionDelete)
	}

	// Unknown names resolve to ("", false) — no accidental registry growth.
	for _, unknown := range []string{"", "vault.file.read", "object:delete", "vault.file.delete.extra"} {
		if a, ok := ActionForPermission(unknown); ok || a != "" {
			t.Fatalf("ActionForPermission(%q) = (%q,%v); want (\"\",false)", unknown, a, ok)
		}
	}

	// The literal must equal the canonical provider-facing name.
	if PermissionVaultFileDelete != "vault.file.delete" {
		t.Fatalf("PermissionVaultFileDelete = %q; want \"vault.file.delete\"", PermissionVaultFileDelete)
	}
}

// T-8 (FR-3 companion): the system delete exemption predicate is tenant exact.
// Both the generic internal system context and the antivirus context are
// trusted; wildcard or mismatched tenants still fail closed.
func TestIsSystemDeleteExempt(t *testing.T) {
	av := Principal{Kind: PrincipalSystem, SubjectID: SystemActorAntivirus, TenantID: "acme"}
	allow := []struct {
		name      string
		principal Principal
		tenant    string
	}{
		{"antivirus exact tenant", av, "acme"},
		{"generic system exact tenant", Principal{Kind: PrincipalSystem, SubjectID: "aero-vault-system", TenantID: "acme"}, "acme"},
	}
	for _, c := range allow {
		if !IsSystemDeleteExempt(c.principal, c.tenant) {
			t.Errorf("IsSystemDeleteExempt(%+v, %q) = false; want true (%s)", c.principal, c.tenant, c.name)
		}
	}
	deny := []struct {
		name      string
		principal Principal
		tenant    string
	}{
		{"antivirus wrong tenant", av, "other"},
		{"antivirus empty tenant principal", Principal{Kind: PrincipalSystem, SubjectID: SystemActorAntivirus}, "acme"},
		{"antivirus wildcard tenant", Principal{Kind: PrincipalSystem, SubjectID: SystemActorAntivirus, TenantID: "*"}, "acme"},
		{"anonymous", Principal{Kind: PrincipalAnonymous, SubjectID: "anon", TenantID: "acme"}, "acme"},
		{"zero-value principal", Principal{}, "acme"},
	}
	for _, c := range deny {
		if IsSystemDeleteExempt(c.principal, c.tenant) {
			t.Errorf("IsSystemDeleteExempt(%+v, %q) = true; want false (%s)", c.principal, c.tenant, c.name)
		}
	}
	if SystemActorAntivirus != "system:antivirus" {
		t.Fatalf("SystemActorAntivirus = %q; want \"system:antivirus\"", SystemActorAntivirus)
	}
}
