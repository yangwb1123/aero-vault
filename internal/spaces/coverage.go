package spaces

import "context"

// CoverageScopeKind distinguishes an explicit tenant from the entire authority.
// Global scope is not represented by a wildcard or a default tenant.
type CoverageScopeKind string

const (
	CoverageTenant CoverageScopeKind = "tenant"
	CoverageGlobal CoverageScopeKind = "global"
)

type CoverageScope struct {
	Kind     CoverageScopeKind
	TenantID string
}

// CoverageState describes binding facts, never a permission decision. Unknown
// is an explicit incomplete observation; the zero value is invalid.
type CoverageState string

const (
	CoverageBound   CoverageState = "bound"
	CoverageUnbound CoverageState = "unbound"
	CoverageUnknown CoverageState = "unknown"
)

// CoverageResult echoes the exact observed scope. Tenant facts apply only to a
// tenant scope; global observations must leave them empty. Witnesses are positive
// binding examples, not a complete inventory. Complete must come from a fresh,
// authoritative observation of the entire scope, never a limited bucket list,
// cached absence, or the number of witnesses. No field grants access.
type CoverageResult struct {
	Scope        CoverageScope
	TenantKnown  bool
	TenantStatus string
	State        CoverageState
	Complete     bool
	Witnesses    []BindingResult
}

// CoverageReader is an independent, unwired, read-only port, not part of Store
// or BindingReader. Each call must observe current authoritative coverage.
// Faults return a zero result and a cause-preserving error. A valid unknown or
// incomplete result is not proof of absence and cannot pass admission.
type CoverageReader interface {
	ProbeSpaceCoverage(ctx context.Context, scope CoverageScope) (CoverageResult, error)
}

func ValidateCoverageScope(scope CoverageScope) error {
	switch scope.Kind {
	case CoverageTenant:
		return ValidateTenant(scope.TenantID)
	case CoverageGlobal:
		if scope.TenantID != "" {
			return invalid("global coverage with tenant")
		}
		return nil
	default:
		return invalid("coverage scope kind")
	}
}

// ValidateCoverageResult checks shape and consistency, not eligibility or the
// truth/freshness of a provider's completeness assertion. Partial bound facts
// are representable, but must not be treated as complete coverage.
func ValidateCoverageResult(result CoverageResult) error {
	if err := ValidateCoverageScope(result.Scope); err != nil {
		return err
	}
	if err := validateCoverageTenant(result); err != nil {
		return err
	}
	if err := validateCoverageState(result); err != nil {
		return err
	}
	return validateCoverageWitnesses(result)
}

// ValidateCoverageObservation also checks the complete request echo, without
// trimming, case-folding, wildcard expansion, or supplying defaults.
func ValidateCoverageObservation(scope CoverageScope, result CoverageResult) error {
	if err := ValidateCoverageScope(scope); err != nil {
		return err
	}
	if err := ValidateCoverageResult(result); err != nil {
		return err
	}
	if result.Scope != scope {
		return invalid("coverage scope echo")
	}
	return nil
}

func validateCoverageTenant(result CoverageResult) error {
	if result.Scope.Kind == CoverageGlobal {
		if result.TenantKnown || result.TenantStatus != "" {
			return invalid("global coverage with tenant facts")
		}
		return nil
	}
	if !result.TenantKnown && result.TenantStatus != "" {
		return invalid("unknown coverage tenant with status")
	}
	return nil
}

func validateCoverageState(result CoverageResult) error {
	switch result.State {
	case CoverageBound:
		if len(result.Witnesses) == 0 {
			return invalid("bound coverage without witness")
		}
	case CoverageUnbound:
		if !result.Complete || len(result.Witnesses) != 0 {
			return invalid("unbound coverage without complete absence")
		}
	case CoverageUnknown:
		if result.Complete || len(result.Witnesses) != 0 {
			return invalid("unknown coverage with definitive facts")
		}
	default:
		return invalid("coverage state")
	}
	return nil
}

func validateCoverageWitnesses(result CoverageResult) error {
	buckets := make(map[[2]string]bool)
	spaces := make(map[[2]string]bool)
	statuses := make(map[string]string)
	for _, witness := range result.Witnesses {
		if err := validateCoverageWitness(result, witness); err != nil {
			return err
		}
		bucket := [2]string{witness.TenantID, witness.Bucket}
		space := [2]string{witness.TenantID, witness.SpaceID}
		if buckets[bucket] || spaces[space] {
			return invalid("duplicate coverage witness")
		}
		if status, seen := statuses[witness.TenantID]; seen && status != witness.TenantStatus {
			return invalid("contradictory coverage tenant status")
		}
		buckets[bucket], spaces[space] = true, true
		statuses[witness.TenantID] = witness.TenantStatus
	}
	return nil
}

func validateCoverageWitness(result CoverageResult, witness BindingResult) error {
	if err := ValidateBindingResult(witness); err != nil {
		return err
	}
	if witness.State != BindingBound {
		return invalid("coverage witness is not bound")
	}
	if result.Scope.Kind == CoverageTenant {
		if witness.TenantID != result.Scope.TenantID || witness.TenantKnown != result.TenantKnown ||
			witness.TenantStatus != result.TenantStatus {
			return invalid("coverage witness tenant mismatch")
		}
	}
	return nil
}
