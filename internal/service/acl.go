package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// Canned ACLs (S3-compatible subset).
const (
	ACLPrivate            = "private"
	ACLPublicRead         = "public-read"
	ACLPublicReadWrite    = "public-read-write"
	ACLAuthenticatedRead  = "authenticated-read"
	pendingACLMetadataKey = "_aero_pending_acl"
	pendingLegalHoldKey   = "_aero_pending_legal_hold"
)

func validACL(acl string) bool {
	switch acl {
	case ACLPrivate, ACLPublicRead, ACLPublicReadWrite, ACLAuthenticatedRead:
		return true
	default:
		return false
	}
}

// ValidateACL validates a canned ACL before a protocol adapter starts a
// multi-step operation such as bucket creation.
func ValidateACL(acl string) error {
	if acl == "" || validACL(acl) {
		return nil
	}
	return fmt.Errorf("%w: invalid canned ACL %q", ErrInvalidArgs, acl)
}

// PublicReadable reports whether a canned ACL grants anonymous read.
func PublicReadable(acl string) bool {
	return acl == ACLPublicRead || acl == ACLPublicReadWrite
}

// SetObjectACL sets an object's canned ACL.
func (s *FileService) SetObjectACL(ctx context.Context, tenant, bucket, key, acl string) error {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return err
	}
	if err := ValidateACL(acl); err != nil || acl == "" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: empty canned ACL", ErrInvalidArgs)
	}
	if _, err := s.objectForAction(ctx, tenant, bucket, key, access.ActionManageACL); err != nil {
		return err
	}
	return s.repo.SetObjectACL(ctx, tenant, bucket, key, acl)
}

// GetObjectACL returns an object's canned ACL.
func (s *FileService) GetObjectACL(ctx context.Context, tenant, bucket, key string) (string, error) {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return "", err
	}
	if _, err := s.objectForAction(ctx, tenant, bucket, key, access.ActionManageACL); err != nil {
		return "", err
	}
	acl, err := s.repo.GetObjectACL(ctx, tenant, bucket, key)
	if errors.Is(err, repository.ErrNotFound) {
		return "", ErrNotFound
	}
	return acl, err
}

// SetBucketACL sets a bucket's canned ACL.
func (s *FileService) SetBucketACL(ctx context.Context, tenant, bucket, acl string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := ValidateACL(acl); err != nil || acl == "" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: empty canned ACL", ErrInvalidArgs)
	}
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketACL(ctx, tenant, bucket, acl)
}

// ObjectPublicReadable reports whether an object may be read anonymously, taking
// both the object ACL and its bucket's ACL into account.
func (s *FileService) ObjectPublicReadable(ctx context.Context, tenant, bucket, key string) bool {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return false
	}
	if acl, err := s.repo.GetObjectACL(ctx, tenant, bucket, key); err == nil && PublicReadable(acl) {
		return true
	}
	if cfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket); err == nil && PublicReadable(cfg.ACL) {
		return true
	}
	return false
}
