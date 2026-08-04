package access

import "context"

// Store is the persistence port owned by the access domain. The SQL repository
// implements it without widening the core object Repository interface.
type Store interface {
	PutDepartment(context.Context, Department) error
	GetDepartment(context.Context, string, string) (Department, error)
	ListDepartments(context.Context, string) ([]Department, error)
	DeleteDepartment(context.Context, string, string) error
	PutDepartmentMember(context.Context, DepartmentMember) error
	DeleteDepartmentMember(context.Context, string, string, string) error
	ListDepartmentMembers(context.Context, string, string) ([]DepartmentMember, error)
	ListSubjectDepartments(context.Context, string, string) ([]string, error)

	PutACLEntry(context.Context, ACLEntry) error
	GetACLEntry(context.Context, string, string) (ACLEntry, error)
	DeleteACLEntry(context.Context, string, string) error
	ListResourceACL(context.Context, string, string, string, ResourceKind) ([]ACLEntry, error)
	ListApplicableACL(context.Context, string, string, string) ([]ACLEntry, error)

	CreateShare(context.Context, Share) error
	GetShare(context.Context, string, string) (Share, error)
	GetShareByTokenHash(context.Context, string) (Share, error)
	ListShares(context.Context, string, string, string) ([]Share, error)
	RevokeShare(context.Context, string, string, string) error
	ConsumeShare(context.Context, string, string) (Share, error)

	PutPublicAsset(context.Context, PublicAsset) error
	GetPublicAsset(context.Context, string) (PublicAsset, error)
	ListPublicAssets(context.Context, string) ([]PublicAsset, error)
	DeletePublicAsset(context.Context, string, string) error
}
