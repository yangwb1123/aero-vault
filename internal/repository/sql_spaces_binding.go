package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

// Deliberately separate from Repository/Store and not captured by wiring.
var _ spaces.BindingReader = (*sqlStore)(nil)

// The requested row, not tenants, drives both joins. Even an unknown tenant
// must observe the exact binding. Each parameter occurs once (I1); CAST also
// makes the derived parameter types explicit for PostgreSQL.
const spaceBindingQuery = `SELECT t.tenant_id, t.status,
       sp.tenant_id, sp.backing_bucket, sp.id, sp.revision
FROM (SELECT CAST($1 AS TEXT) AS tenant_id,
             CAST($2 AS TEXT) AS bucket_name) AS requested
LEFT JOIN tenants AS t
       ON t.tenant_id = requested.tenant_id
LEFT JOIN spaces AS sp
       ON sp.tenant_id = requested.tenant_id
      AND sp.backing_bucket = requested.bucket_name`

// ProbeSpaceBinding observes tenant and binding facts in one SELECT snapshot.
// Unlike GetSpaceByBucket it does not require an active, registered tenant.
func (s *sqlStore) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	if err := spaces.ValidateBindingInput(tenant, bucket); err != nil {
		return spaces.BindingResult{}, err
	}
	// QueryRow.Scan includes query errors and database/sql's Rows.Close result.
	result, err := scanSpaceBinding(s.db.QueryRowContext(ctx, s.rebind(spaceBindingQuery), tenant, bucket), tenant, bucket)
	if err != nil {
		return spaces.BindingResult{}, spaceBindingSQLError(ctx, "probe binding", err)
	}
	return result, nil
}

func scanSpaceBinding(row spaceScanner, tenant, bucket string) (spaces.BindingResult, error) {
	var tenantID, status, spaceTenant, spaceBucket, id sql.NullString
	var revision sql.NullInt64
	if err := row.Scan(&tenantID, &status, &spaceTenant, &spaceBucket, &id, &revision); err != nil {
		return spaces.BindingResult{}, err
	}
	if err := validateBindingTenantRow(tenantID, status, tenant); err != nil {
		return spaces.BindingResult{}, err
	}
	result := spaces.BindingResult{TenantID: tenant, Bucket: bucket, TenantKnown: tenantID.Valid,
		TenantStatus: status.String, State: spaces.BindingUnbound}
	result, err := spaceBindingFromRow(result, spaceTenant, spaceBucket, id, revision)
	if err != nil {
		return spaces.BindingResult{}, err
	}
	if err := spaces.ValidateBindingResult(result); err != nil {
		return spaces.BindingResult{}, fmt.Errorf("binding observation: %w: %w", spaces.ErrPersistence, err)
	}
	return result, nil
}

func validateBindingTenantRow(id, status sql.NullString, requested string) error {
	if id.Valid != status.Valid {
		return invalidBindingRow("partial tenant NULL")
	}
	if id.Valid && id.String != requested {
		return invalidBindingRow("tenant mismatch")
	}
	return nil
}

func spaceBindingFromRow(result spaces.BindingResult, tenant, bucket, id sql.NullString, revision sql.NullInt64) (spaces.BindingResult, error) {
	present := 0
	for _, valid := range []bool{tenant.Valid, bucket.Valid, id.Valid, revision.Valid} {
		if valid {
			present++
		}
	}
	if present == 0 {
		return result, nil
	}
	if present != 4 {
		return spaces.BindingResult{}, invalidBindingRow("partial Space NULL")
	}
	if tenant.String != result.TenantID || bucket.String != result.Bucket {
		return spaces.BindingResult{}, invalidBindingRow("Space scope mismatch")
	}
	result.State, result.SpaceID, result.Revision = spaces.BindingBound, id.String, revision.Int64
	return result, nil
}

func invalidBindingRow(detail string) error {
	return fmt.Errorf("binding observation %s: %w: %w", detail, spaces.ErrPersistence, spaces.ErrInvalidArgument)
}

func spaceBindingSQLError(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	// The SELECT always produces one row under the existing unique keys.
	// Unexpected no-row must not use the old mapper's ErrNotFound category.
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("spaces %s: %w: %w", op, spaces.ErrPersistence, err)
	}
	return spaceSQLError(ctx, op, err)
}
