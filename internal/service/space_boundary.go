package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// SpaceBoundary is deny-only admission, independent of the authorization
// provider. Success means continue the existing policy, never permission.
type SpaceBoundary interface {
	CheckBucket(context.Context, string, string) error
	CheckCoverage(context.Context, spaces.CoverageScope) error
}

// SpaceResourceReader retains exact persisted identities across wrappers.
// An ID-only cleanup port or a latest-key lookup is not a substitute.
type SpaceResourceReader interface {
	GetObjectByID(context.Context, int64) (repository.Object, error)
	ListChunksForObject(context.Context, int64) ([]repository.Chunk, error)
	ListEventsAfter(context.Context, string, int64, int) ([]repository.Event, error)
}

type SpaceBoundaryReaders struct {
	Binding   spaces.BindingReader
	Coverage  spaces.CoverageReader
	Jobs      repository.JobReader
	Resources SpaceResourceReader
}

// DiscoverSpaceBoundaryReaders examines only the actual value supplied. Roots
// must capture these ports before wrapping writes; it never unwraps a repository.
func DiscoverSpaceBoundaryReaders(repo repository.Repository) SpaceBoundaryReaders {
	binding, _ := repo.(spaces.BindingReader)
	coverage, _ := repo.(spaces.CoverageReader)
	jobs, ok := repo.(repository.JobReader)
	if !ok {
		jobs, _ = repository.NewJobReader(repo)
	}
	return SpaceBoundaryReaders{Binding: binding, Coverage: coverage, Jobs: jobs, Resources: repo}
}

type serviceSpaceBoundary struct {
	readers SpaceBoundaryReaders
	core    *access.ResourceBoundary
}

func NewSpaceBoundary(readers SpaceBoundaryReaders) SpaceBoundary {
	return &serviceSpaceBoundary{readers: readers, core: access.NewResourceBoundary(readers.Binding, readers.Coverage)}
}

func (b *serviceSpaceBoundary) CheckBucket(ctx context.Context, tenant, bucket string) error {
	if b == nil {
		return spaceBoundaryFault(errors.New("missing service boundary"))
	}
	return b.core.CheckBucket(ctx, tenant, bucket)
}

func (b *serviceSpaceBoundary) CheckCoverage(ctx context.Context, scope spaces.CoverageScope) error {
	if b == nil {
		return spaceBoundaryFault(errors.New("missing service boundary"))
	}
	return b.core.CheckCoverage(ctx, scope)
}

func (b *serviceSpaceBoundary) Validate() error {
	if b == nil {
		return spaceBoundaryFault(errors.New("missing service boundary"))
	}
	return ValidateSpaceBoundaryReaders(b.readers)
}

// ValidateSpaceBoundaryReaders is a side-effect-free production preflight.
// Completeness and exact echoes are still validated afresh at each observation.
func ValidateSpaceBoundaryReaders(readers SpaceBoundaryReaders) error {
	for _, port := range []any{readers.Binding, readers.Coverage, readers.Jobs, readers.Resources} {
		if spacePortMissing(port) {
			return spaceBoundaryFault(errors.New("missing space boundary capability"))
		}
	}
	return nil
}

// ValidateSpaceBoundary rejects the optional legacy nil at production startup.
func (s *FileService) ValidateSpaceBoundary() error {
	if s == nil || spacePortMissing(s.spaceBoundary) {
		return spaceBoundaryFault(errors.New("missing service space boundary"))
	}
	validator, ok := s.spaceBoundary.(interface{ Validate() error })
	if !ok || spacePortMissing(validator) {
		return spaceBoundaryFault(errors.New("boundary has no production preflight"))
	}
	return validator.Validate()
}

// CheckSpaceBucket admits a literal protocol resource without granting access
// or normalizing its scope. Direct-read adapters still run their own policy.
func (s *FileService) CheckSpaceBucket(ctx context.Context, tenant, bucket string) error {
	return s.admitBucket(ctx, tenant, bucket)
}

func (s *FileService) admitBucket(ctx context.Context, tenant, bucket string) error {
	if s.spaceBoundary == nil {
		return nil // explicitly optional legacy capability, not a production default
	}
	if spacePortMissing(s.spaceBoundary) {
		return spaceBoundaryFault(errors.New("typed-nil service boundary"))
	}
	return serviceBoundaryError(s.spaceBoundary.CheckBucket(ctx, tenant, bucket))
}

func (s *FileService) admitCoverage(ctx context.Context, scope spaces.CoverageScope) error {
	if s.spaceBoundary == nil {
		return nil
	}
	if spacePortMissing(s.spaceBoundary) {
		return spaceBoundaryFault(errors.New("typed-nil service boundary"))
	}
	return serviceBoundaryError(s.spaceBoundary.CheckCoverage(ctx, scope))
}

func serviceBoundaryError(err error) error {
	if access.IsResourceBoundaryDenied(err) {
		if errors.Is(err, spaces.ErrTenantUnavailable) {
			return errors.Join(ErrForbidden, ErrTenantDisabled, err)
		}
		return errors.Join(ErrForbidden, err)
	}
	return err
}

func spaceBoundaryFault(cause error) error {
	return fmt.Errorf("%w: %w", access.ErrResourceBoundaryFault, cause)
}

func repositoryObjectMissing(err error) bool {
	return !access.IsResourceBoundaryFault(err) && errors.Is(err, repository.ErrNotFound)
}

func batchActionError(err error) string {
	if access.IsResourceBoundaryFault(err) {
		return access.ErrResourceBoundaryFault.Error()
	}
	return err.Error()
}

func ordinaryAccessDenial(err error) bool {
	return !access.IsResourceBoundaryFault(err) && !errors.Is(err, spaces.ErrTenantUnavailable) &&
		!errors.Is(err, ErrTenantDisabled) && (errors.Is(err, ErrForbidden) || access.IsResourceBoundaryDenied(err))
}

func spacePortMissing(port any) bool {
	if port == nil {
		return true
	}
	v := reflect.ValueOf(port)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// Bucket-name enumeration filters individual bound buckets, rather than
// treating one bound bucket as a tenant-wide denial. Tenant status is still a
// mandatory independent check, including for system callers and empty names.
func (s *FileService) admitBucketNamesTenant(ctx context.Context, tenant string) error {
	if s.spaceBoundary == nil {
		return nil
	}
	if spacePortMissing(s.spaceBoundary) || ctx == nil {
		return spaceBoundaryFault(ErrInvalidArgs)
	}
	if err := spaces.ValidateTenant(tenant); err != nil {
		return spaceBoundaryFault(err)
	}
	if err := ctx.Err(); err != nil {
		return spaceBoundaryFault(err)
	}
	record, known, err := s.repo.GetTenant(ctx, tenant)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return spaceBoundaryFault(err)
	}
	return validateBucketNamesTenantStatus(tenant, record, known)
}

func validateBucketNamesTenantStatus(tenant string, record repository.TenantRecord, known bool) error {
	if known && record.TenantID != tenant {
		return spaceBoundaryFault(errors.New("bucket-list tenant scope echo"))
	}
	if !known && !reflect.ValueOf(record).IsZero() {
		return spaceBoundaryFault(errors.New("unknown bucket-list tenant with facts"))
	}
	if known && record.Status != "active" {
		return errors.Join(ErrForbidden, ErrTenantDisabled, spaces.ErrTenantUnavailable)
	}
	return nil
}

func (s *FileService) validateBoundaryObjects(objects []repository.Object, tenant, bucket, key string) error {
	if s.spaceBoundary == nil {
		return nil
	}
	for _, obj := range objects {
		if obj.TenantID != tenant || obj.Bucket != bucket || (key != "" && obj.Key != key) || obj.ID <= 0 {
			return spaceBoundaryFault(errors.New("object observation scope conflict"))
		}
		if err := validateKey(obj.Key); err != nil {
			return spaceBoundaryFault(err)
		}
	}
	return nil
}

func tenantCoverage(tenant string) spaces.CoverageScope {
	return spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: tenant}
}
