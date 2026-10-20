package spaces

import "context"

// Store is an internal, tenant-explicit relational port. It does not grant
// permissions, publish Spaces, write objects, or connect to an identity source.
// Commands commit root/member/history together; creation also strictly creates
// new bucket metadata. Conflicts/competition may be explicitly retried in a new
// transaction; no implicit command replay or exactly-once guarantee is offered.
type Store interface {
	CreateSpace(context.Context, CreateCommand) (Space, error)
	GetSpace(ctx context.Context, tenant, id string) (Space, error)
	GetSpaceByBucket(ctx context.Context, tenant, bucket string) (Space, error)
	GetPersonalSpace(ctx context.Context, tenant string, subject SubjectRef) (Space, error)
	GetSpaceMember(ctx context.Context, tenant, id string, subject SubjectRef) (Member, error)
	ListSpaceMembers(ctx context.Context, tenant, id string, page MemberPage) ([]Member, error)
	// ListSpacesForSubject lists active relationships, not content visibility.
	ListSpacesForSubject(ctx context.Context, tenant string, subject SubjectRef, page SpacePage) ([]Space, error)
	ListSpaceHistory(ctx context.Context, tenant, id string, page HistoryPage) ([]History, error)
	ChangeSpaceMember(context.Context, MemberCommand) (int64, error)
}
