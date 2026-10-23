package events

import (
	"context"
	"errors"
	"reflect"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

var (
	// ErrResourceBoundaryFault is an observation/configuration failure. It is
	// never a legacy-allow signal, even when a cause looks like NotFound/Denied.
	ErrResourceBoundaryFault = access.ErrResourceBoundaryFault
	// ErrResourceBoundaryDenied is an explicit refusal that may be filtered by
	// ordinary authorization policy. Faults always take priority over it.
	ErrResourceBoundaryDenied = access.ErrDenied
	// ErrSpaceBound means the exact bucket is Space-bound; event consumers must
	// not deliver, transport or notify through the legacy path.
	ErrSpaceBound = access.ErrSpaceBound
	// ErrResourceBoundaryUnavailable reports that discovery found no binding
	// reader. It is a configuration fault, not an unbound observation.
	ErrResourceBoundaryUnavailable = errors.New("event resource boundary: binding reader unavailable")
)

// BoundaryOutcome is a successful observation only when it is BoundaryUnbound
// or BoundaryBound. The zero value is invalid and accompanies a fault.
type BoundaryOutcome string

const (
	BoundaryUnknown BoundaryOutcome = ""
	BoundaryUnbound BoundaryOutcome = "unbound"
	BoundaryBound   BoundaryOutcome = "bound"
)

// ResourceBoundary adapts the frozen shared deny-only admission contract to
// event consumers. It is not an authorization grant and has no enable flag or
// principal shortcut: an unbound fact merely permits the caller's existing
// policy. Known non-active tenants remain refused.
type ResourceBoundary struct {
	core *access.ResourceBoundary
}

// NewResourceBoundary captures an explicit narrow reader and adapts it through
// the frozen shared boundary. A nil or typed-nil reader fails at observation
// time; it never becomes legacy Allow.
func NewResourceBoundary(binding spaces.BindingReader) *ResourceBoundary {
	return NewResourceBoundaryFromAccess(access.NewResourceBoundary(binding, nil))
}

// NewResourceBoundaryFromAccess wraps a retained shared boundary. A nil or
// typed-nil boundary faults at observation time rather than allowing egress.
func NewResourceBoundaryFromAccess(core *access.ResourceBoundary) *ResourceBoundary {
	return &ResourceBoundary{core: core}
}

// NewResourceBoundaryFromRepository discovers the existing binding port on a
// raw repository captured before durable wrappers. A wrapper without the port
// is a fault, not evidence of an unbound bucket.
func NewResourceBoundaryFromRepository(repo repository.Repository) (*ResourceBoundary, error) {
	if eventBoundaryMissing(repo) {
		return nil, ErrResourceBoundaryUnavailable
	}
	reader, ok := repo.(spaces.BindingReader)
	if !ok || eventBoundaryMissing(reader) {
		return nil, ErrResourceBoundaryUnavailable
	}
	return NewResourceBoundary(reader), nil
}

// Classify observes exactly tenant/bucket. A bound result is returned without
// error but must be refused by the caller. A known non-active tenant returns
// BoundaryUnbound plus an explicit denial. Invalid input, context, missing
// capabilities, malformed values, echo mismatches and reader failures return
// the zero outcome plus a fault.
func (b *ResourceBoundary) Classify(ctx context.Context, tenant, bucket string) (BoundaryOutcome, error) {
	if eventBoundaryMissing(ctx) {
		return BoundaryUnknown, newResourceBoundaryFault(errors.New("missing boundary context"))
	}
	if err := ctx.Err(); err != nil {
		return BoundaryUnknown, newResourceBoundaryFault(err)
	}
	if b == nil || b.core == nil {
		return BoundaryUnknown, newResourceBoundaryFault(errors.New("missing access boundary"))
	}
	err := b.core.CheckBucket(ctx, tenant, bucket)
	if err == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return BoundaryUnknown, newResourceBoundaryFault(ctxErr)
		}
		return BoundaryUnbound, nil
	}
	if access.IsResourceBoundaryFault(err) {
		return BoundaryUnknown, newResourceBoundaryFault(err)
	}
	if access.IsResourceBoundaryDenied(err) {
		if errors.Is(err, access.ErrSpaceBound) {
			return BoundaryBound, nil
		}
		return BoundaryUnbound, errors.Join(ErrResourceBoundaryDenied, err)
	}
	return BoundaryUnknown, newResourceBoundaryFault(err)
}

// CheckBucket is the error-only form used by admission call sites: unbound is
// the only success, bound is a denial, and every fault remains a fault.
func (b *ResourceBoundary) CheckBucket(ctx context.Context, tenant, bucket string) error {
	outcome, err := b.Classify(ctx, tenant, bucket)
	if err != nil {
		return err
	}
	if outcome == BoundaryBound {
		return errors.Join(ErrResourceBoundaryDenied, ErrSpaceBound)
	}
	if outcome != BoundaryUnbound {
		return newResourceBoundaryFault(errors.New("invalid boundary outcome"))
	}
	return nil
}

// IsResourceBoundaryFault must be checked before generic NotFound/Denied
// classification because those errors may be preserved causes of a fault.
func IsResourceBoundaryFault(err error) bool {
	return access.IsResourceBoundaryFault(err) ||
		errors.Is(err, ErrResourceBoundaryUnavailable)
}

// IsResourceBoundaryDenied recognizes only explicit boundary refusals. Faults
// are never treated as ordinary denials.
func IsResourceBoundaryDenied(err error) bool {
	return !IsResourceBoundaryFault(err) && errors.Is(err, ErrResourceBoundaryDenied)
}

func eventBoundaryMissing(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// The public text is deliberately safe; Unwrap retains the diagnostic chain
// for internal classification without stringifying raw causes at the edge.
type eventResourceBoundaryFault struct{ cause error }

func newResourceBoundaryFault(cause error) error {
	return &eventResourceBoundaryFault{cause: cause}
}

func (*eventResourceBoundaryFault) Error() string { return ErrResourceBoundaryFault.Error() }
func (e *eventResourceBoundaryFault) Unwrap() error {
	return e.cause
}
func (*eventResourceBoundaryFault) Is(target error) bool {
	return target == ErrResourceBoundaryFault
}
