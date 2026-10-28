package ai

import (
	"context"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// SearchResourceBoundary adapts the frozen Core deny-only boundary to search
// callers. It is not an authorization grant: an unbound result merely permits
// the existing tenant/ACL policy, while a bound or fault result stops delivery.
// The zero value is unconfigured and therefore preserves legacy Search tests;
// an explicitly configured boundary with missing/typed-nil readers faults.
type SearchResourceBoundary struct {
	core     *access.ResourceBoundary
	binding  spaces.BindingReader
	coverage spaces.CoverageReader
	readers  bool
}

func newSearchResourceBoundary(binding spaces.BindingReader, coverage spaces.CoverageReader) *SearchResourceBoundary {
	return &SearchResourceBoundary{core: access.NewResourceBoundary(binding, coverage), binding: binding, coverage: coverage, readers: true}
}

func newSearchResourceBoundaryFromCore(core *access.ResourceBoundary) *SearchResourceBoundary {
	return &SearchResourceBoundary{core: core}
}

// check selects the exact bucket observation when the request names a bucket.
// An empty bucket is a tenant-wide query, so only authoritative tenant Coverage
// may prove absence; a limited ListBuckets result is never used.
func (b *SearchResourceBoundary) check(ctx context.Context, tenant, bucket string) error {
	if b == nil {
		return nil
	}
	if b.core == nil {
		return fmt.Errorf("%w: missing search resource boundary", access.ErrResourceBoundaryFault)
	}
	if bucket != "" {
		return b.core.CheckBucket(ctx, tenant, bucket)
	}
	return b.core.CheckCoverage(ctx, spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: tenant})
}

// checkP0 is the fresh pre-retrieval observation. checkP1 is a separate fresh
// observation after the candidate set has been fully materialized.
func (b *SearchResourceBoundary) checkP0(ctx context.Context, tenant, bucket string) error {
	return b.check(ctx, tenant, bucket)
}

func (b *SearchResourceBoundary) checkP1(ctx context.Context, tenant, bucket string) error {
	return b.check(ctx, tenant, bucket)
}

// IsSearchResourceBoundaryFault must be checked before generic NotFound/Denied
// classification because those causes may be joined to an observation fault.
func IsSearchResourceBoundaryFault(err error) bool {
	return access.IsResourceBoundaryFault(err)
}

// IsSearchResourceBoundaryDenied recognizes explicit refusals only; faults are
// never converted into filtered results.
func IsSearchResourceBoundaryDenied(err error) bool {
	return !IsSearchResourceBoundaryFault(err) && access.IsResourceBoundaryDenied(err)
}

// WithResourceBoundary installs the production deny-bound guard. Callers pass
// the raw narrow readers captured before repository wrappers. Explicit nil or
// typed-nil readers remain configured and fault at observation time.
func (s *Search) WithResourceBoundary(binding spaces.BindingReader, coverage spaces.CoverageReader) *Search {
	s.boundary = newSearchResourceBoundary(binding, coverage)
	s.canonical = true
	return s
}

// WithResourceBoundaryCore injects an already-built shared Core boundary. A
// nil core is a configured fault, never an implicit legacy Allow.
func (s *Search) WithResourceBoundaryCore(core *access.ResourceBoundary) *Search {
	s.boundary = newSearchResourceBoundaryFromCore(core)
	s.canonical = true
	return s
}

// WithCanonicalResources retains the raw authoritative reader before repository
// wrappers. An explicitly missing/typed-nil reader is a configured fault.
func (s *Search) WithCanonicalResources(resources service.SpaceResourceReader) *Search {
	s.resources, s.resourcesExplicit = resources, true
	s.canonical = true
	return s
}

// ValidateResourceBoundary is side-effect-free startup validation. A Core is
// opaque: its existence alone cannot prove either reader capability. Legacy
// constructors remain usable, but production must explicitly inject readers
// and the FileService authorizer, independently of Access.Enabled.
func (s *Search) ValidateResourceBoundary() error {
	if s == nil || s.boundary == nil {
		return searchBoundaryFault("missing search boundary")
	}
	if nilLike(s.boundary.binding) || nilLike(s.boundary.coverage) || s.boundary.core == nil {
		return searchBoundaryFault("missing search boundary readers")
	}
	if !s.resourcesExplicit || nilLike(s.resources) || nilLike(s.hitAuth) {
		return searchBoundaryFault("missing canonical resources or hit authorizer")
	}
	return s.configuredResourceError()
}

func searchBoundaryFault(message string) error {
	return fmt.Errorf("%w: %s", access.ErrResourceBoundaryFault, message)
}

func searchContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("search: nil context")
	}
	return ctx.Err()
}

func (s *Search) configuredResourceError() error {
	// Legacy retrieval can fail without canonical ports; every opt-in guarded
	// consumer still requires both readers before doing any work.
	if s == nil || (s.canonical && (nilLike(s.resources) || nilLike(s.repo))) {
		return searchBoundaryFault("missing canonical resource reader")
	}
	if s.boundary != nil {
		if err := s.boundary.configuredError(); err != nil {
			return err
		}
	}
	if s.hitAuthConfigured && nilLike(s.hitAuth) {
		return searchBoundaryFault("missing hit authorizer")
	}
	return s.configuredBackendError()
}

func (s *Search) configuredBackendError() error {
	for _, port := range []any{s.rerank, s.vindex, s.lexical} {
		if port != nil && nilLike(port) {
			return searchBoundaryFault("typed-nil search backend")
		}
	}
	return nil
}

func (b *SearchResourceBoundary) configuredError() error {
	if b.core == nil {
		return searchBoundaryFault("missing search resource boundary")
	}
	if b.readers && (nilLike(b.binding) || nilLike(b.coverage)) {
		return searchBoundaryFault("missing search boundary reader")
	}
	return nil
}

func (s *Search) boundaryP0(ctx context.Context, tenant, bucket string) error {
	if err := searchContextError(ctx); err != nil {
		return err
	}
	if err := s.configuredResourceError(); err != nil {
		return err
	}
	if s.boundary == nil {
		return nil
	}
	return s.boundary.checkP0(ctx, tenant, bucket)
}

func (s *Search) boundaryP1(ctx context.Context, tenant, bucket string) error {
	if err := searchContextError(ctx); err != nil {
		return err
	}
	if s.boundary == nil {
		return nil
	}
	return s.boundary.checkP1(ctx, tenant, bucket)
}
