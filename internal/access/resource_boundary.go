package access

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

var (
	ErrResourceBoundaryFault = errors.New("resource boundary observation failed")
	ErrSpaceBound            = errors.New("resource is bound to a space")
)

// ResourceBoundary only denies Space-bound resources or uncertain observations.
// A nil check result means continue the original tenant/authorization policy,
// never permission granted. It has no principal, system bypass, enabled switch,
// cache, or consumer wiring. Its zero value fails closed.
type ResourceBoundary struct {
	binding  spaces.BindingReader
	coverage spaces.CoverageReader
}

// NewResourceBoundary keeps the two independent capabilities explicit. Missing
// or typed-nil readers fault at their respective check; neither port substitutes
// for the other. This constructor installs no runtime boundary.
func NewResourceBoundary(binding spaces.BindingReader, coverage spaces.CoverageReader) *ResourceBoundary {
	return &ResourceBoundary{binding: binding, coverage: coverage}
}

// CheckBucket observes the literal tenant/bucket before any legacy shortcut.
// Unknown tenants can only continue after an authoritative, exact unbound fact.
func (b *ResourceBoundary) CheckBucket(ctx context.Context, tenant, bucket string) error {
	if err := boundaryContextError(ctx); err != nil {
		return boundaryFault("binding", err)
	}
	if err := spaces.ValidateBindingInput(tenant, bucket); err != nil {
		return boundaryFault("binding", err)
	}
	if b == nil || boundaryReaderMissing(b.binding) {
		return boundaryFault("binding", errors.New("missing binding reader"))
	}
	result, err := b.binding.ProbeSpaceBinding(ctx, tenant, bucket)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return boundaryFault("binding", err)
	}
	if err := spaces.ValidateBindingResult(result); err != nil {
		return boundaryFault("binding", err)
	}
	if result.TenantID != tenant || result.Bucket != bucket {
		return boundaryFault("binding", fmt.Errorf("%w: binding scope echo", ErrInvalidArgument))
	}
	if result.State == spaces.BindingBound {
		return errors.Join(ErrDenied, ErrSpaceBound)
	}
	return boundaryTenantAdmission(result.TenantKnown, result.TenantStatus)
}

// CheckCoverage performs one fresh coverage observation. Consumers must call it
// for P0, fully materialize D, then call it again for P1 before delivery; this
// primitive neither orchestrates D nor makes DB/Blob/egress atomic. A complete
// global absence says nothing about tenant eligibility or other permissions.
func (b *ResourceBoundary) CheckCoverage(ctx context.Context, scope spaces.CoverageScope) error {
	if err := boundaryContextError(ctx); err != nil {
		return boundaryFault("coverage", err)
	}
	if err := spaces.ValidateCoverageScope(scope); err != nil {
		return boundaryFault("coverage", err)
	}
	if b == nil || boundaryReaderMissing(b.coverage) {
		return boundaryFault("coverage", errors.New("missing coverage reader"))
	}
	result, err := b.coverage.ProbeSpaceCoverage(ctx, scope)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return boundaryFault("coverage", err)
	}
	if err := spaces.ValidateCoverageObservation(scope, result); err != nil {
		return boundaryFault("coverage", err)
	}
	if !result.Complete {
		return boundaryFault("coverage", errors.New("incomplete coverage observation"))
	}
	if result.State == spaces.CoverageBound {
		return errors.Join(ErrDenied, ErrSpaceBound)
	}
	if scope.Kind == spaces.CoverageTenant {
		return boundaryTenantAdmission(result.TenantKnown, result.TenantStatus)
	}
	return nil
}

// IsResourceBoundaryFault must be checked before nested Denied/NotFound causes.
// Causes remain reachable with errors.Is/As, including cancellation/deadline;
// protocol adapters must retain their timeout policy and sanitize error output.
func IsResourceBoundaryFault(err error) bool {
	return errors.Is(err, ErrResourceBoundaryFault)
}

// IsResourceBoundaryDenied excludes faults even when a provider or outer
// wrapper joins ErrDenied to them. Such faults must not become filtered results.
func IsResourceBoundaryDenied(err error) bool {
	return !IsResourceBoundaryFault(err) && errors.Is(err, ErrDenied)
}

type resourceBoundaryFault struct {
	operation string
	cause     error
}

// Error deliberately omits provider text and resource identities. Unwrap keeps
// the original cause available to internal classifiers, not client responses.
func (e *resourceBoundaryFault) Error() string {
	return ErrResourceBoundaryFault.Error() + ": " + e.operation
}

func (e *resourceBoundaryFault) Unwrap() error { return e.cause }

func (e *resourceBoundaryFault) Is(target error) bool { return target == ErrResourceBoundaryFault }

func boundaryFault(operation string, cause error) error {
	return &resourceBoundaryFault{operation: operation, cause: cause}
}

func boundaryTenantAdmission(known bool, status string) error {
	if known && status != "active" {
		return errors.Join(ErrDenied, spaces.ErrTenantUnavailable)
	}
	return nil
}

func boundaryContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil boundary context", ErrInvalidArgument)
	}
	return ctx.Err()
}

func boundaryReaderMissing(reader any) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
