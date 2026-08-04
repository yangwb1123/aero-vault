// Package access owns Aero Vault's resource authorization model. Identity and
// coarse application roles may come from Snaplink, while file ACLs, shares,
// departments, and published assets remain local domain state.
package access

import (
	"errors"
	"time"
)

var (
	ErrDisabled        = errors.New("access control is disabled")
	ErrDenied          = errors.New("access denied")
	ErrInvalidArgument = errors.New("invalid access-control argument")
	ErrNotFound        = errors.New("access-control record not found")
	ErrShareExpired    = errors.New("share expired or exhausted")
	ErrBadPassword     = errors.New("share password is invalid")
)

type PrincipalKind string

const (
	PrincipalUser      PrincipalKind = "user"
	PrincipalService   PrincipalKind = "service"
	PrincipalAnonymous PrincipalKind = "anonymous"
	PrincipalShare     PrincipalKind = "share"
	PrincipalPublic    PrincipalKind = "public_asset"
	PrincipalSystem    PrincipalKind = "system"
)

// Principal is the normalized caller identity consumed by the local PDP.
// Adapters must not leak Snaplink-specific types past this boundary.
type Principal struct {
	SubjectID  string
	TenantID   string
	Kind       PrincipalKind
	Roles      []string
	Groups     []string
	Scopes     []string
	Capability *Capability
}

type Capability struct {
	ID       string
	TenantID string
	Bucket   string
	Key      string
	Actions  []Action
}

type ResourceKind string

const (
	ResourceBucket ResourceKind = "bucket"
	ResourceFolder ResourceKind = "folder"
	ResourceObject ResourceKind = "object"
)

type Resource struct {
	TenantID string
	Bucket   string
	Key      string
	Kind     ResourceKind
	OwnerID  string
}

type Action string

const (
	ActionList      Action = "object:list"
	ActionRead      Action = "object:read"
	ActionPreview   Action = "object:preview"
	ActionDownload  Action = "object:download"
	ActionCreate    Action = "object:create"
	ActionWrite     Action = "object:write"
	ActionDelete    Action = "object:delete"
	ActionRestore   Action = "object:restore"
	ActionShare     Action = "object:share"
	ActionManageACL Action = "object:manage_acl"
	ActionPublish   Action = "asset:publish"
	ActionExport    Action = "object:export"
	ActionAll       Action = "*"
)

func ValidAction(action Action) bool {
	switch action {
	case ActionList, ActionRead, ActionPreview, ActionDownload, ActionCreate,
		ActionWrite, ActionDelete, ActionRestore, ActionShare, ActionManageACL,
		ActionPublish, ActionExport, ActionAll:
		return true
	default:
		return false
	}
}

type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

type Department struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DepartmentMember struct {
	TenantID     string    `json:"tenant_id"`
	DepartmentID string    `json:"department_id"`
	SubjectID    string    `json:"subject_id"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

type PrincipalType string

const (
	PrincipalTypeUser          PrincipalType = "user"
	PrincipalTypeDepartment    PrincipalType = "department"
	PrincipalTypeGroup         PrincipalType = "group"
	PrincipalTypeRole          PrincipalType = "role"
	PrincipalTypeAuthenticated PrincipalType = "authenticated"
	PrincipalTypeEveryone      PrincipalType = "everyone"
)

type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

type ACLEntry struct {
	ID            string        `json:"id"`
	TenantID      string        `json:"tenant_id"`
	Bucket        string        `json:"bucket"`
	Key           string        `json:"key,omitempty"`
	ResourceKind  ResourceKind  `json:"resource_kind"`
	PrincipalType PrincipalType `json:"principal_type"`
	PrincipalID   string        `json:"principal_id,omitempty"`
	Action        Action        `json:"action"`
	Effect        Effect        `json:"effect"`
	Inherit       bool          `json:"inherit"`
	CreatedBy     string        `json:"created_by"`
	CreatedAt     time.Time     `json:"created_at"`
	OwnerID       string        `json:"-"`
}

type Share struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	Bucket        string    `json:"bucket"`
	Key           string    `json:"key"`
	Name          string    `json:"name,omitempty"`
	TokenHash     string    `json:"-"`
	PasswordMAC   string    `json:"-"`
	AllowPreview  bool      `json:"allow_preview"`
	AllowDownload bool      `json:"allow_download"`
	MaxUses       int64     `json:"max_uses,omitempty"`
	UseCount      int64     `json:"use_count"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	RevokedAt     time.Time `json:"revoked_at,omitempty"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	OwnerID       string    `json:"-"`
}

type PublicAsset struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	Bucket       string    `json:"bucket"`
	Key          string    `json:"key"`
	Slug         string    `json:"slug"`
	CacheControl string    `json:"cache_control"`
	PublishedBy  string    `json:"published_by"`
	PublishedAt  time.Time `json:"published_at"`
	OwnerID      string    `json:"-"`
}
