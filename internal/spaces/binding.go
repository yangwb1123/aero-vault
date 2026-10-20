package spaces

import "context"

// BindingState describes a database fact, never an authorization decision.
// Its zero value is invalid; failures use a non-nil error, not a third success.
type BindingState string

const (
	BindingBound   BindingState = "bound"
	BindingUnbound BindingState = "unbound"
)

// BindingResult is one complete observation of an explicit tenant and bucket.
// TenantStatus is raw persisted text (including empty text), not a policy enum.
// Neither a binding nor its absence grants access or proves a bucket exists.
type BindingResult struct {
	TenantID     string
	Bucket       string
	TenantKnown  bool
	TenantStatus string
	State        BindingState
	SpaceID      string
	Revision     int64
}

// BindingReader is a separate, unwired, read-only side port. It does not widen
// Store. Success requires a valid result; every fault returns a complete zero
// result and preserves its cause in the non-nil error.
type BindingReader interface {
	ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (BindingResult, error)
}

// ValidateBindingInput never supplies defaults, trims, or case-folds names.
func ValidateBindingInput(tenant, bucket string) error {
	if err := ValidateTenant(tenant); err != nil {
		return err
	}
	if err := ValidateIdentifier(bucket); err != nil {
		return err
	}
	if bucket == "*" {
		return invalid("bucket wildcard")
	}
	return nil
}

// ValidateBindingResult checks success shape only, not tenant eligibility.
// SQL adapters must separately distinguish NULL from present empty text.
func ValidateBindingResult(result BindingResult) error {
	if err := ValidateBindingInput(result.TenantID, result.Bucket); err != nil {
		return err
	}
	if !result.TenantKnown && result.TenantStatus != "" {
		return invalid("unknown tenant with status")
	}
	switch result.State {
	case BindingBound:
		return validateBoundResult(result)
	case BindingUnbound:
		if result.SpaceID != "" || result.Revision != 0 {
			return invalid("unbound with Space facts")
		}
		return nil
	default:
		return invalid("binding state")
	}
}

func validateBoundResult(result BindingResult) error {
	if !result.TenantKnown {
		return invalid("bound with unknown tenant")
	}
	if err := ValidateID(result.SpaceID); err != nil {
		return err
	}
	if result.Revision <= 0 {
		return invalid("binding revision")
	}
	return nil
}
