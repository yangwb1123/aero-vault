package service

import (
	"context"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// FileListView narrows a listing; it never grants access to an object.
type FileListView string

const (
	FileListAll  FileListView = "all"
	FileListMine FileListView = "mine"
)

type FileListOptions struct {
	View    FileListView
	Deleted bool
}

// ListWithOptions lists readable objects, optionally restricted to the current
// user's or service's trusted owner identity. Ownership is not an ACL bypass.
func (s *FileService) ListWithOptions(
	ctx context.Context, tenant, bucket, prefix, marker string, limit int,
	opts FileListOptions,
) (repository.ListPage, error) {
	tenant, bucket = defaults(tenant, bucket)
	fetch := s.repo.ListObjects
	if opts.Deleted {
		fetch = s.repo.ListDeletedObjects
	}
	if opts.View == "" || opts.View == FileListAll {
		return s.listAuthorizedObjects(ctx, tenant, bucket, prefix, marker, limit, fetch)
	}
	if opts.View != FileListMine {
		return repository.ListPage{}, fmt.Errorf("%w: view must be all or mine", ErrInvalidArgs)
	}
	principal, ok := access.PrincipalFrom(ctx)
	if !ok || principal.SubjectID == "" ||
		(principal.Kind != access.PrincipalUser && principal.Kind != access.PrincipalService) ||
		(principal.TenantID != tenant && principal.TenantID != "*") {
		return repository.ListPage{}, fmt.Errorf("%w: mine view requires a tenant-bound user or service identity", ErrForbidden)
	}
	matches := func(obj repository.Object) bool {
		return obj.TenantID == tenant && obj.Bucket == bucket && ObjectOwner(obj) == principal.SubjectID
	}
	return s.listObjectsMatching(ctx, tenant, bucket, prefix, marker, limit, fetch, matches)
}
