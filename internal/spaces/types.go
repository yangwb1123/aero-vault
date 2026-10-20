// Package spaces defines unconnected persistence contracts, not application
// authorization or proof of employee eligibility. No production entry point uses it.
package spaces

import "time"

type SubjectKind string

const (
	User    SubjectKind = "user"
	Service SubjectKind = "service"
)

// SubjectRef is an opaque, case-sensitive identity in an explicit namespace.
// A user label alone is not proof of a human or of employment.
type SubjectRef struct {
	Source string
	Kind   SubjectKind
	ID     string
}

type Kind string

const (
	Personal Kind = "personal"
	Team     Kind = "team"
)

type Role string

const (
	Viewer Role = "viewer"
	Editor Role = "editor"
	Admin  Role = "admin"
)

type MemberState string

const (
	Active  MemberState = "active"
	Revoked MemberState = "revoked"
)

type MemberAction string

const (
	AddMember        MemberAction = "member.add"
	ChangeRole       MemberAction = "member.change_role"
	RevokeMember     MemberAction = "member.revoke"
	ReactivateMember MemberAction = "member.reactivate"
)

type Space struct {
	TenantID        string
	ID              string
	Kind            Kind
	Name            string
	BackingBucket   string
	PersonalSubject *SubjectRef
	Revision        int64
	CreatedBy       SubjectRef
	UpdatedBy       SubjectRef
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Member struct {
	TenantID        string
	SpaceID         string
	Subject         SubjectRef
	Role            Role
	State           MemberState
	ChangedRevision int64
	ChangedBy       SubjectRef
	JoinedAt        time.Time
	UpdatedAt       time.Time
	RevokedAt       *time.Time
}

type History struct {
	TenantID       string
	ID             string
	SpaceID        string
	CommandID      string
	CommandType    string
	Actor          SubjectRef
	Target         SubjectRef
	BeforeRevision int64
	AfterRevision  int64
	// Empty before values represent SQL NULL, only for create/add.
	BeforeRole  Role
	AfterRole   Role
	BeforeState MemberState
	AfterState  MemberState
	Reason      string
	RequestID   string
	OccurredAt  time.Time
}

type CreateCommand struct {
	TenantID  string
	ID        string
	Kind      Kind
	Name      string
	Actor     SubjectRef
	CommandID string
	Reason    string
	RequestID string
}

type MemberCommand struct {
	TenantID         string
	SpaceID          string
	ExpectedRevision int64
	Actor            SubjectRef
	Target           SubjectRef
	Action           MemberAction
	// Revoke requires an empty Role; other actions require a valid Role.
	Role      Role
	CommandID string
	Reason    string
	RequestID string
}

// Pages are exclusive keyset cursors. Use the last returned identity/ID/revision
// as the next cursor. An empty result is the end, not a missing Space.
type MemberPage struct {
	After *SubjectRef
	Limit int
}

type SpacePage struct {
	AfterID string
	Limit   int
}

type HistoryPage struct {
	AfterRevision int64
	Limit         int
}
