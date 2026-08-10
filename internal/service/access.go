package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

const ownerMetadataKey = "_aero_owner"

func ObjectOwner(obj repository.Object) string { return obj.Metadata[ownerMetadataKey] }

func (s *FileService) AuthorizeObjectAction(
	ctx context.Context,
	action access.Action,
	obj repository.Object,
) error {
	return s.authorizeObject(ctx, action, obj)
}

func (s *FileService) CanReadObject(ctx context.Context, tenant, bucket, key string) error {
	_, err := s.Stat(ctx, tenant, bucket, key)
	return err
}

func (s *FileService) objectForAction(
	ctx context.Context,
	tenant, bucket, key string,
	action access.Action,
) (repository.Object, error) {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return repository.Object{}, err
	}
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if errors.Is(err, repository.ErrNotFound) {
		return repository.Object{}, ErrNotFound
	}
	if err != nil {
		return repository.Object{}, err
	}
	if err := s.authorizeObject(ctx, action, obj); err != nil {
		return repository.Object{}, err
	}
	return obj, nil
}

func (s *FileService) authorizeBucket(
	ctx context.Context,
	action access.Action,
	tenant, bucket string,
) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.authorizePath(ctx, action, tenant, bucket, "")
}

func (s *FileService) filterAuthorizedVersions(
	ctx context.Context,
	versions []repository.Object,
) ([]repository.Object, error) {
	if s.authorizer == nil {
		return versions, nil
	}
	out := make([]repository.Object, 0, len(versions))
	for _, version := range versions {
		err := s.authorizeObject(ctx, access.ActionRead, version)
		if errors.Is(err, ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, version)
	}
	return out, nil
}

func (s *FileService) authorize(
	ctx context.Context,
	action access.Action,
	resource access.Resource,
) error {
	if err := s.requireActiveTenant(ctx, resource.TenantID); err != nil {
		return err
	}
	if s.authorizer == nil {
		if action == access.ActionDelete && !s.deleteFailOpen {
			principal, _ := access.PrincipalFrom(ctx)
			if !access.IsSystemDeleteExempt(principal, resource.TenantID) {
				return fmt.Errorf("%w: no authorization provider configured", ErrForbidden)
			}
		}
		return nil
	}
	principal, _ := access.PrincipalFrom(ctx)
	decision, err := s.authorizer.Authorize(ctx, principal, action, resource)
	if err != nil {
		return fmt.Errorf("authorization decision: %w", err)
	}
	if !decision.Allowed {
		return fmt.Errorf("%w: %s", ErrForbidden, decision.Reason)
	}
	return nil
}

func (s *FileService) requireActiveTenant(ctx context.Context, tenant string) error {
	if !s.tenantStatus || middleware.TenantStatusVerified(ctx, tenant) {
		return nil
	}
	if principal, ok := access.PrincipalFrom(ctx); ok && principal.Kind == access.PrincipalSystem {
		return nil
	}
	record, found, err := s.repo.GetTenant(ctx, tenant)
	if err != nil {
		return fmt.Errorf("tenant status: %w", err)
	}
	if found && record.Status == "disabled" {
		return fmt.Errorf("%w: %w", ErrForbidden, ErrTenantDisabled)
	}
	return nil
}

func (s *FileService) authorizeObject(ctx context.Context, action access.Action, obj repository.Object) error {
	return s.authorize(ctx, action, access.Resource{
		TenantID: obj.TenantID,
		Bucket:   obj.Bucket,
		Key:      obj.Key,
		Kind:     objectResourceKind(obj.Key),
		OwnerID:  obj.Metadata[ownerMetadataKey],
	})
}

func (s *FileService) authorizePath(
	ctx context.Context,
	action access.Action,
	tenant, bucket, key string,
) error {
	return s.authorize(ctx, action, access.Resource{
		TenantID: tenant,
		Bucket:   bucket,
		Key:      key,
		Kind:     objectResourceKind(key),
	})
}

func objectResourceKind(key string) access.ResourceKind {
	if key == "" {
		return access.ResourceBucket
	}
	if strings.HasSuffix(key, "/") {
		return access.ResourceFolder
	}
	return access.ResourceObject
}

func (s *FileService) preparePutAccess(
	ctx context.Context,
	tenant, bucket, key string,
	metadata map[string]string,
) (map[string]string, error) {
	existing, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err == nil {
		if err := s.authorizeObject(ctx, access.ActionWrite, existing); err != nil {
			return nil, err
		}
		return metadataWithOwner(metadata, existing.Metadata[ownerMetadataKey]), nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	if err := s.authorizePath(ctx, access.ActionCreate, tenant, bucket, key); err != nil {
		return nil, err
	}
	principal, _ := access.PrincipalFrom(ctx)
	return metadataWithOwner(metadata, ownerForPrincipal(principal)), nil
}

func ownerForPrincipal(principal access.Principal) string {
	if principal.Kind == access.PrincipalUser || principal.Kind == access.PrincipalService {
		return principal.SubjectID
	}
	return ""
}

func metadataWithOwner(metadata map[string]string, owner string) map[string]string {
	if owner == "" {
		return metadata
	}
	copied := cloneMetadata(metadata)
	copied[ownerMetadataKey] = owner
	return copied
}

type objectPageFetcher func(
	context.Context, string, string, string, string, int,
) (repository.ListPage, error)

func (s *FileService) listAuthorizedObjects(
	ctx context.Context,
	tenant, bucket, prefix, marker string,
	limit int,
	fetch objectPageFetcher,
) (repository.ListPage, error) {
	if err := s.requireActiveTenant(ctx, tenant); err != nil {
		return repository.ListPage{}, err
	}
	if s.authorizer == nil {
		return fetch(ctx, tenant, bucket, prefix, marker, limit)
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	objects, err := s.collectAuthorizedObjects(
		ctx, tenant, bucket, prefix, marker, limit+1, fetch,
	)
	if err != nil {
		return repository.ListPage{}, err
	}
	page := repository.ListPage{Objects: objects}
	if len(objects) > limit {
		page.Objects = objects[:limit]
		page.HasMore = true
		page.NextMarker = page.Objects[len(page.Objects)-1].Key
	}
	return page, nil
}

func (s *FileService) collectAuthorizedObjects(
	ctx context.Context,
	tenant, bucket, prefix, marker string,
	wanted int,
	fetch objectPageFetcher,
) ([]repository.Object, error) {
	objects := make([]repository.Object, 0, wanted)
	cursor := marker
	for len(objects) < wanted {
		page, err := fetch(ctx, tenant, bucket, prefix, cursor, 1000)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Objects {
			if err := s.appendAuthorizedObject(ctx, obj, &objects); err != nil {
				return nil, err
			}
			if len(objects) == wanted {
				return objects, nil
			}
		}
		if !page.HasMore || page.NextMarker == "" || page.NextMarker == cursor {
			return objects, nil
		}
		cursor = page.NextMarker
	}
	return objects, nil
}

func (s *FileService) appendAuthorizedObject(
	ctx context.Context, obj repository.Object, objects *[]repository.Object,
) error {
	err := s.authorizeObject(ctx, access.ActionRead, obj)
	if errors.Is(err, ErrForbidden) {
		return nil
	}
	if err != nil {
		return err
	}
	*objects = append(*objects, obj)
	return nil
}
