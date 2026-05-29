package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// Canned ACLs (S3-compatible subset).
const (
	ACLPrivate           = "private"
	ACLPublicRead        = "public-read"
	ACLPublicReadWrite   = "public-read-write"
	ACLAuthenticatedRead = "authenticated-read"
)

func validACL(acl string) bool {
	switch acl {
	case ACLPrivate, ACLPublicRead, ACLPublicReadWrite, ACLAuthenticatedRead:
		return true
	default:
		return false
	}
}

// PublicReadable reports whether a canned ACL grants anonymous read.
func PublicReadable(acl string) bool {
	return acl == ACLPublicRead || acl == ACLPublicReadWrite
}

// SetObjectACL sets an object's canned ACL.
func (s *FileService) SetObjectACL(ctx context.Context, tenant, bucket, key, acl string) error {
	tenant, bucket = defaults(tenant, bucket)
	if !validACL(acl) {
		return fmt.Errorf("%w: invalid canned ACL %q", ErrInvalidArgs, acl)
	}
	return s.repo.SetObjectACL(ctx, tenant, bucket, key, acl)
}

// GetObjectACL returns an object's canned ACL.
func (s *FileService) GetObjectACL(ctx context.Context, tenant, bucket, key string) (string, error) {
	tenant, bucket = defaults(tenant, bucket)
	acl, err := s.repo.GetObjectACL(ctx, tenant, bucket, key)
	if errors.Is(err, repository.ErrNotFound) {
		return "", ErrNotFound
	}
	return acl, err
}

// SetBucketACL sets a bucket's canned ACL.
func (s *FileService) SetBucketACL(ctx context.Context, tenant, bucket, acl string) error {
	tenant, bucket = defaults(tenant, bucket)
	if !validACL(acl) {
		return fmt.Errorf("%w: invalid canned ACL %q", ErrInvalidArgs, acl)
	}
	return s.repo.SetBucketACL(ctx, tenant, bucket, acl)
}

// ObjectPublicReadable reports whether an object may be read anonymously, taking
// both the object ACL and its bucket's ACL into account.
func (s *FileService) ObjectPublicReadable(ctx context.Context, tenant, bucket, key string) bool {
	tenant, bucket = defaults(tenant, bucket)
	if acl, err := s.repo.GetObjectACL(ctx, tenant, bucket, key); err == nil && PublicReadable(acl) {
		return true
	}
	if cfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket); err == nil && PublicReadable(cfg.ACL) {
		return true
	}
	return false
}
