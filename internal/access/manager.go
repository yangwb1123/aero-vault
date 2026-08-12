package access

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultDeny   = "deny"
	DefaultTenant = "tenant"
)

type Config struct {
	Enabled       bool
	DefaultPolicy string
	ShareSecret   []byte
	// DeleteFailOpen opts out of the fail-closed delete gate (FR-2): when
	// true and the manager is disabled, ActionDelete is allowed again
	// (legacy baseline). The zero value is fail-closed: a disabled manager
	// denies ActionDelete.
	DeleteFailOpen bool
}

type Manager struct {
	store Store
	cfg   Config
}

func NewManager(store Store, cfg Config) (*Manager, error) {
	if store == nil {
		return nil, errors.New("access: store is required")
	}
	if cfg.DefaultPolicy == "" {
		cfg.DefaultPolicy = DefaultDeny
	}
	if cfg.DefaultPolicy != DefaultDeny && cfg.DefaultPolicy != DefaultTenant {
		return nil, fmt.Errorf("access: default policy must be %q or %q", DefaultDeny, DefaultTenant)
	}
	if len(cfg.ShareSecret) < 32 {
		return nil, errors.New("access: share secret must be at least 32 bytes")
	}
	return &Manager{store: store, cfg: cfg}, nil
}

func (m *Manager) Enabled() bool { return m != nil && m.cfg.Enabled }

func (m *Manager) CreateDepartment(ctx context.Context, department Department) (Department, error) {
	if err := validateDepartment(department); err != nil {
		return Department{}, err
	}
	if department.ParentID != "" {
		if _, err := m.store.GetDepartment(ctx, department.TenantID, department.ParentID); err != nil {
			return Department{}, fmt.Errorf("%w: parent department: %v", ErrInvalidArgument, err)
		}
	}
	now := time.Now().UTC()
	if department.ID == "" {
		department.ID = uuid.NewString()
	}
	department.CreatedAt, department.UpdatedAt = now, now
	if err := m.store.PutDepartment(ctx, department); err != nil {
		return Department{}, err
	}
	return department, nil
}

func validateDepartment(department Department) error {
	if strings.TrimSpace(department.TenantID) == "" || strings.TrimSpace(department.Name) == "" {
		return fmt.Errorf("%w: tenant_id and name are required", ErrInvalidArgument)
	}
	if len(department.Name) > 200 || department.ParentID == department.ID && department.ID != "" {
		return fmt.Errorf("%w: invalid department name or parent", ErrInvalidArgument)
	}
	return nil
}

func (m *Manager) GetDepartment(ctx context.Context, tenant, id string) (Department, error) {
	return m.store.GetDepartment(ctx, tenant, id)
}

func (m *Manager) ListDepartments(ctx context.Context, tenant string) ([]Department, error) {
	return m.store.ListDepartments(ctx, tenant)
}

func (m *Manager) DeleteDepartment(ctx context.Context, tenant, id string) error {
	return m.store.DeleteDepartment(ctx, tenant, id)
}

func (m *Manager) PutDepartmentMember(ctx context.Context, member DepartmentMember) error {
	if member.TenantID == "" || member.DepartmentID == "" || member.SubjectID == "" {
		return fmt.Errorf("%w: tenant_id, department_id and subject_id are required", ErrInvalidArgument)
	}
	if member.Role == "" {
		member.Role = "member"
	}
	if member.Role != "member" && member.Role != "manager" {
		return fmt.Errorf("%w: department role must be member or manager", ErrInvalidArgument)
	}
	if _, err := m.store.GetDepartment(ctx, member.TenantID, member.DepartmentID); err != nil {
		return fmt.Errorf("%w: department: %v", ErrInvalidArgument, err)
	}
	member.CreatedAt = time.Now().UTC()
	return m.store.PutDepartmentMember(ctx, member)
}

func (m *Manager) DeleteDepartmentMember(ctx context.Context, tenant, departmentID, subjectID string) error {
	return m.store.DeleteDepartmentMember(ctx, tenant, departmentID, subjectID)
}

func (m *Manager) ListDepartmentMembers(ctx context.Context, tenant, departmentID string) ([]DepartmentMember, error) {
	return m.store.ListDepartmentMembers(ctx, tenant, departmentID)
}

func (m *Manager) PutACL(ctx context.Context, entry ACLEntry) (ACLEntry, error) {
	normalizeACLResource(&entry)
	if err := validateACL(entry); err != nil {
		return ACLEntry{}, err
	}
	if err := m.validateACLPrincipal(ctx, entry); err != nil {
		return ACLEntry{}, err
	}
	if err := m.require(ctx, ActionManageACL, entry.resource()); err != nil {
		return ACLEntry{}, err
	}
	if entry.ID == "" {
		existing, err := m.findMatchingACL(ctx, entry)
		if err != nil {
			return ACLEntry{}, err
		}
		if existing.ID != "" {
			entry.ID, entry.CreatedBy, entry.CreatedAt = existing.ID, existing.CreatedBy, existing.CreatedAt
		} else {
			entry.ID = uuid.NewString()
		}
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedBy = subjectFromContext(ctx)
		entry.CreatedAt = time.Now().UTC()
	}
	if err := m.store.PutACLEntry(ctx, entry); err != nil {
		return ACLEntry{}, err
	}
	return entry, nil
}

func (m *Manager) validateACLPrincipal(ctx context.Context, entry ACLEntry) error {
	if entry.PrincipalType != PrincipalTypeDepartment {
		return nil
	}
	if _, err := m.store.GetDepartment(ctx, entry.TenantID, entry.PrincipalID); err != nil {
		return fmt.Errorf("%w: department principal: %v", ErrInvalidArgument, err)
	}
	return nil
}

func (m *Manager) findMatchingACL(ctx context.Context, entry ACLEntry) (ACLEntry, error) {
	entries, err := m.store.ListResourceACL(
		ctx, entry.TenantID, entry.Bucket, entry.Key, entry.ResourceKind,
	)
	if err != nil {
		return ACLEntry{}, err
	}
	for _, existing := range entries {
		if existing.PrincipalType == entry.PrincipalType && existing.PrincipalID == entry.PrincipalID &&
			existing.Action == entry.Action {
			return existing, nil
		}
	}
	return ACLEntry{}, nil
}

func normalizeACLResource(entry *ACLEntry) {
	switch entry.ResourceKind {
	case ResourceBucket:
		entry.Key = ""
	case ResourceFolder:
		entry.Key = strings.TrimPrefix(entry.Key, "/")
		if entry.Key != "" && !strings.HasSuffix(entry.Key, "/") {
			entry.Key += "/"
		}
	default:
		entry.Key = strings.TrimPrefix(entry.Key, "/")
	}
}

func validateACL(entry ACLEntry) error {
	if entry.TenantID == "" || entry.Bucket == "" || entry.Action == "" {
		return fmt.Errorf("%w: tenant_id, bucket and action are required", ErrInvalidArgument)
	}
	if entry.ResourceKind != ResourceBucket && entry.ResourceKind != ResourceFolder && entry.ResourceKind != ResourceObject {
		return fmt.Errorf("%w: invalid resource_kind", ErrInvalidArgument)
	}
	// Defense-in-depth (REQ-2): folder keys participate in prefix matching
	// (ListApplicableACL), so '%'/'_' must never be stored — they would act
	// as SQL LIKE wildcards if a LIKE-based clause is ever re-introduced.
	// Object/bucket keys match exactly and are not restricted. Keys are
	// already normalized (normalizeACLResource) when this is called.
	if entry.ResourceKind == ResourceFolder && strings.ContainsAny(entry.Key, "%_") {
		return fmt.Errorf("%w: folder ACL key %q contains %% or _ (SQL LIKE wildcard metacharacters)", ErrInvalidArgument, entry.Key)
	}
	if entry.Effect != EffectAllow && entry.Effect != EffectDeny {
		return fmt.Errorf("%w: effect must be allow or deny", ErrInvalidArgument)
	}
	if !validPrincipalType(entry.PrincipalType) || principalIDRequired(entry.PrincipalType) && entry.PrincipalID == "" {
		return fmt.Errorf("%w: principal is incomplete", ErrInvalidArgument)
	}
	if !ValidAction(entry.Action) {
		return fmt.Errorf("%w: unsupported action", ErrInvalidArgument)
	}
	if entry.ResourceKind == ResourceFolder &&
		strings.ContainsAny(entry.Key, "%_") {
		return fmt.Errorf("%w: folder ACL key %q must not contain %% or _ (SQL wildcard metacharacters)",
			ErrInvalidArgument, entry.Key)
	}
	return nil
}

func validPrincipalType(kind PrincipalType) bool {
	switch kind {
	case PrincipalTypeUser, PrincipalTypeDepartment, PrincipalTypeGroup,
		PrincipalTypeRole, PrincipalTypeAuthenticated, PrincipalTypeEveryone:
		return true
	default:
		return false
	}
}

func principalIDRequired(kind PrincipalType) bool {
	return kind != PrincipalTypeEveryone && kind != PrincipalTypeAuthenticated
}

func (entry ACLEntry) resource() Resource {
	return Resource{
		TenantID: entry.TenantID, Bucket: entry.Bucket, Key: entry.Key,
		Kind: entry.ResourceKind, OwnerID: entry.OwnerID,
	}
}

func (m *Manager) DeleteACL(ctx context.Context, tenant, id string) error {
	entry, err := m.store.GetACLEntry(ctx, tenant, id)
	if err != nil {
		return err
	}
	if subjectFromContext(ctx) != entry.CreatedBy {
		if err := m.require(ctx, ActionManageACL, entry.resource()); err != nil {
			return err
		}
	}
	return m.store.DeleteACLEntry(ctx, tenant, id)
}

func (m *Manager) ListACL(ctx context.Context, resource Resource) ([]ACLEntry, error) {
	if err := m.require(ctx, ActionManageACL, resource); err != nil {
		return nil, err
	}
	return m.store.ListResourceACL(ctx, resource.TenantID, resource.Bucket, resource.Key, resource.Kind)
}

func (m *Manager) require(ctx context.Context, action Action, resource Resource) error {
	return authorizeOrDenied(ctx, m, action, resource)
}

func subjectFromContext(ctx context.Context) string {
	principal, _ := PrincipalFrom(ctx)
	return principal.SubjectID
}

func randomToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) passwordMAC(shareID, password string) string {
	if password == "" {
		return ""
	}
	mac := hmac.New(sha256.New, m.cfg.ShareSecret)
	_, _ = mac.Write([]byte("share-password\n" + shareID + "\n" + password))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
