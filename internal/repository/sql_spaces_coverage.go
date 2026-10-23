package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

// This independent read capability is deliberately not embedded in Repository
// or spaces.Store, and is not installed in any runtime admission path.
var _ spaces.CoverageReader = (*sqlStore)(nil)

// A requested row drives the joins even for an unknown tenant. Enumerating
// every binding (not buckets) proves coverage without a limit or active-status
// filter. Each parameter occurs once, in binding order (I1).
const spaceCoverageTenantQuery = `SELECT t.tenant_id, t.status,
       sp.tenant_id, sp.backing_bucket, sp.id, sp.revision
FROM (SELECT CAST($1 AS TEXT) AS tenant_id) AS requested
LEFT JOIN tenants AS t ON t.tenant_id = requested.tenant_id
LEFT JOIN spaces AS sp ON sp.tenant_id = requested.tenant_id
ORDER BY sp.backing_bucket, sp.id`

// The singleton also makes an empty authority produce an explicit NULL row.
// Driving from spaces rather than tenants exposes orphan bindings as faults,
// rather than hiding them behind an inner join or a tenant-status predicate.
const spaceCoverageGlobalQuery = `SELECT t.tenant_id, t.status,
       sp.tenant_id, sp.backing_bucket, sp.id, sp.revision
FROM (SELECT 1 AS observation) AS requested
LEFT JOIN spaces AS sp ON TRUE
LEFT JOIN tenants AS t ON t.tenant_id = sp.tenant_id
ORDER BY sp.tenant_id, sp.backing_bucket, sp.id`

// ProbeSpaceCoverage performs a fresh, standalone SELECT on every call. No
// cache or retained transaction can carry P0's absence into a later P1 probe.
// Completeness means this statement's entire scope was observed successfully;
// it is neither an authorization grant nor a DB+Blob atomicity guarantee.
func (s *sqlStore) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	if err := spaces.ValidateCoverageScope(scope); err != nil {
		return spaces.CoverageResult{}, err
	}
	query, args := spaceCoverageGlobalQuery, []any(nil)
	if scope.Kind == spaces.CoverageTenant {
		query, args = spaceCoverageTenantQuery, []any{scope.TenantID}
	}
	var result spaces.CoverageResult
	rows, err := s.db.QueryContext(ctx, s.rebind(query), args...)
	if err == nil {
		result, err = scanSpaceCoverage(rows, scope)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return spaces.CoverageResult{}, spaceCoverageSQLError(ctx, err)
	}
	return result, nil
}

type spaceCoverageRows interface {
	spaceScanner
	Next() bool
	Err() error
	Close() error
}

func scanSpaceCoverage(rows spaceCoverageRows, scope spaces.CoverageScope) (result spaces.CoverageResult, err error) {
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			result, err = spaces.CoverageResult{}, errors.Join(err, closeErr)
		}
	}()
	observation := spaceCoverageObservation{result: spaces.CoverageResult{Scope: scope, State: spaces.CoverageUnbound}}
	for rows.Next() {
		witness, err := scanSpaceCoverageRow(rows, scope)
		if err != nil {
			return spaces.CoverageResult{}, err
		}
		if err := observation.add(witness); err != nil {
			return spaces.CoverageResult{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return spaces.CoverageResult{}, err
	}
	if !observation.observed {
		return spaces.CoverageResult{}, sql.ErrNoRows
	}
	observation.result.Complete = true
	if err := spaces.ValidateCoverageObservation(scope, observation.result); err != nil {
		return spaces.CoverageResult{}, err
	}
	return observation.result, nil
}

// An unbound row here is only an internal NULL sentinel: it has no bucket and
// is never included among the public result's bound witnesses.
func scanSpaceCoverageRow(row spaceScanner, scope spaces.CoverageScope) (spaces.BindingResult, error) {
	var tenantID, status, spaceTenant, bucket, id sql.NullString
	var revision sql.NullInt64
	if err := row.Scan(&tenantID, &status, &spaceTenant, &bucket, &id, &revision); err != nil {
		return spaces.BindingResult{}, err
	}
	tenant := scope.TenantID
	if scope.Kind == spaces.CoverageGlobal {
		tenant = spaceTenant.String
	}
	if err := validateBindingTenantRow(tenantID, status, tenant); err != nil {
		return spaces.BindingResult{}, err
	}
	witness := spaces.BindingResult{TenantID: tenant, Bucket: bucket.String, TenantKnown: tenantID.Valid,
		TenantStatus: status.String, State: spaces.BindingUnbound}
	witness, err := spaceBindingFromRow(witness, spaceTenant, bucket, id, revision)
	if err != nil {
		return spaces.BindingResult{}, err
	}
	if witness.State == spaces.BindingBound {
		if err := spaces.ValidateBindingResult(witness); err != nil {
			return spaces.BindingResult{}, err
		}
	} else if scope.Kind == spaces.CoverageGlobal && witness.TenantKnown {
		return spaces.BindingResult{}, invalidCoverageRow("global absence with tenant facts")
	}
	return witness, nil
}

type spaceCoverageObservation struct {
	result   spaces.CoverageResult
	observed bool
	unbound  bool
}

func (o *spaceCoverageObservation) add(witness spaces.BindingResult) error {
	if o.observed {
		if o.unbound || witness.State == spaces.BindingUnbound {
			return invalidCoverageRow("mixed or duplicate absence")
		}
		if o.result.Scope.Kind == spaces.CoverageTenant &&
			(o.result.TenantKnown != witness.TenantKnown || o.result.TenantStatus != witness.TenantStatus) {
			return invalidCoverageRow("contradictory tenant facts")
		}
	}
	if o.result.Scope.Kind == spaces.CoverageTenant {
		o.result.TenantKnown, o.result.TenantStatus = witness.TenantKnown, witness.TenantStatus
	}
	o.observed, o.unbound = true, witness.State == spaces.BindingUnbound
	if !o.unbound {
		o.result.State = spaces.CoverageBound
		o.result.Witnesses = append(o.result.Witnesses, witness)
	}
	return nil
}

func invalidCoverageRow(detail string) error {
	return fmt.Errorf("coverage observation %s: %w", detail, spaces.ErrInvalidArgument)
}

func spaceCoverageSQLError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	// Do not map unexpected NoRows (or a NotFound cause) to successful absence.
	// Persistence remains the fault class while every underlying cause survives.
	return fmt.Errorf("spaces probe coverage: %w: %w", spaces.ErrPersistence, err)
}
