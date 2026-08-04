package access

import (
	"context"
	"errors"
	"slices"
	"strings"
)

type Authorizer interface {
	Authorize(context.Context, Principal, Action, Resource) (Decision, error)
}

func (m *Manager) Authorize(
	ctx context.Context,
	principal Principal,
	action Action,
	resource Resource,
) (Decision, error) {
	if !m.cfg.Enabled {
		return Decision{Allowed: true, Reason: "access_control_disabled"}, nil
	}
	if principal.Kind == PrincipalSystem {
		return Decision{Allowed: true, Reason: "trusted_system"}, nil
	}
	if principal.SubjectID == "" {
		return denied("missing_principal"), nil
	}
	if !tenantMatches(principal, resource) {
		return denied("tenant_mismatch"), nil
	}
	entries, err := m.store.ListApplicableACL(ctx, resource.TenantID, resource.Bucket, resource.Key)
	if err != nil {
		return denied("acl_store_error"), err
	}
	departments, err := m.store.ListSubjectDepartments(ctx, resource.TenantID, principal.SubjectID)
	if err != nil {
		return denied("directory_store_error"), err
	}
	matching := matchingEntries(entries, principal, departments, action)
	if hasEffect(matching, EffectDeny) {
		return denied("explicit_deny"), nil
	}
	if hasEffect(matching, EffectAllow) {
		return Decision{Allowed: true, Reason: "acl_allow"}, nil
	}
	if capabilityAllows(principal.Capability, resource, action) {
		return Decision{Allowed: true, Reason: "capability"}, nil
	}
	if principal.SubjectID != "" && principal.SubjectID == resource.OwnerID {
		return Decision{Allowed: true, Reason: "owner"}, nil
	}
	if isAdministrator(principal) {
		return Decision{Allowed: true, Reason: "administrator"}, nil
	}
	if len(entries) > 0 {
		return denied("resource_acl_no_match"), nil
	}
	if m.cfg.DefaultPolicy == DefaultTenant && scopeAllows(principal, action) {
		return Decision{Allowed: true, Reason: "tenant_default"}, nil
	}
	return denied("default_deny"), nil
}

func denied(reason string) Decision { return Decision{Allowed: false, Reason: reason} }

func tenantMatches(p Principal, r Resource) bool {
	return p.TenantID == "*" || (p.TenantID != "" && p.TenantID == r.TenantID)
}

func matchingEntries(entries []ACLEntry, p Principal, departments []string, action Action) []ACLEntry {
	out := make([]ACLEntry, 0, len(entries))
	for _, entry := range entries {
		if actionMatches(entry.Action, action) && principalMatches(entry, p, departments) {
			out = append(out, entry)
		}
	}
	return out
}

func actionMatches(granted, wanted Action) bool {
	if granted == ActionAll || granted == wanted {
		return true
	}
	if wanted == ActionPreview || wanted == ActionDownload {
		return granted == ActionRead
	}
	return false
}

func principalMatches(entry ACLEntry, p Principal, departments []string) bool {
	switch entry.PrincipalType {
	case PrincipalTypeEveryone:
		return true
	case PrincipalTypeAuthenticated:
		return p.Kind != PrincipalAnonymous && p.Kind != PrincipalPublic && p.Kind != PrincipalShare
	case PrincipalTypeUser:
		return entry.PrincipalID == p.SubjectID
	case PrincipalTypeDepartment:
		return slices.Contains(departments, entry.PrincipalID)
	case PrincipalTypeGroup:
		return slices.Contains(p.Groups, entry.PrincipalID)
	case PrincipalTypeRole:
		return slices.Contains(p.Roles, entry.PrincipalID)
	default:
		return false
	}
}

func hasEffect(entries []ACLEntry, effect Effect) bool {
	return slices.ContainsFunc(entries, func(entry ACLEntry) bool { return entry.Effect == effect })
}

func capabilityAllows(capability *Capability, resource Resource, action Action) bool {
	if capability == nil || capability.TenantID != resource.TenantID || capability.Bucket != resource.Bucket {
		return false
	}
	if capability.Key != resource.Key {
		return false
	}
	return slices.ContainsFunc(capability.Actions, func(granted Action) bool {
		return actionMatches(granted, action)
	})
}

func isAdministrator(principal Principal) bool {
	if principal.TenantID == "*" || slices.Contains(principal.Scopes, "admin") {
		return true
	}
	for _, role := range principal.Roles {
		if role == "vault.tenant_admin" || role == "vault.file_admin" {
			return true
		}
	}
	return false
}

func IsAdministrator(principal Principal) bool { return isAdministrator(principal) }

func scopeAllows(principal Principal, action Action) bool {
	if slices.Contains(principal.Scopes, "admin") {
		return true
	}
	if action == ActionManageACL {
		return false
	}
	if action == ActionShare {
		return slices.Contains(principal.Scopes, "share")
	}
	if action == ActionPublish {
		return slices.Contains(principal.Scopes, "publish") || slices.ContainsFunc(principal.Roles, func(role string) bool {
			return role == "vault.publisher" || role == "vault.publisher_admin"
		})
	}
	read := action == ActionList || action == ActionRead || action == ActionPreview ||
		action == ActionDownload || action == ActionExport
	wanted := "write"
	if read {
		wanted = "read"
	}
	if slices.Contains(principal.Scopes, wanted) {
		return true
	}
	return slices.ContainsFunc(principal.Roles, func(role string) bool {
		return role == "vault.member" && read || strings.HasPrefix(role, "vault.publisher")
	})
}

func authorizeOrDenied(ctx context.Context, authz Authorizer, action Action, resource Resource) error {
	principal, _ := PrincipalFrom(ctx)
	decision, err := authz.Authorize(ctx, principal, action, resource)
	if err != nil {
		return errors.Join(ErrDenied, err)
	}
	if !decision.Allowed {
		return errors.Join(ErrDenied, errors.New(decision.Reason))
	}
	return nil
}
