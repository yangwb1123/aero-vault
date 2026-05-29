package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const apiKeyCols = `token_hash, tenant_id, scopes, label, created_at, expires_at, last_used_at`

// PutAPIKey upserts an API-key record keyed by its token hash. On first insert
// created_at is set to now; on conflict the original created_at is preserved
// (it is excluded from the UPDATE SET) while the mutable fields are refreshed.
func (s *sqlStore) PutAPIKey(ctx context.Context, k APIKeyRecord) error {
	k.TenantID = defaultTenant(k.TenantID)
	created := k.CreatedAt
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO api_keys (token_hash, tenant_id, scopes, label, created_at, expires_at, last_used_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (token_hash) DO UPDATE SET
		   tenant_id  = $8,
		   scopes     = $9,
		   label      = $10,
		   expires_at = $11`),
		k.TokenHash, k.TenantID, k.Scopes, k.Label, created, k.ExpiresAt, k.LastUsedAt,
		k.TenantID, k.Scopes, k.Label, k.ExpiresAt)
	return err
}

// GetAPIKeyByHash loads the record for a token hash. The bool is false (with a
// nil error) when no row exists.
func (s *sqlStore) GetAPIKeyByHash(ctx context.Context, hash string) (APIKeyRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+apiKeyCols+` FROM api_keys WHERE token_hash=$1`), hash)
	var rec APIKeyRecord
	if err := row.Scan(&rec.TokenHash, &rec.TenantID, &rec.Scopes, &rec.Label, &rec.CreatedAt, &rec.ExpiresAt, &rec.LastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIKeyRecord{}, false, nil
		}
		return APIKeyRecord{}, false, err
	}
	return rec, true, nil
}

// DeleteAPIKeyByHash removes a key by its token hash, reporting whether a row
// was actually deleted.
func (s *sqlStore) DeleteAPIKeyByHash(ctx context.Context, hash string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rebind(
		`DELETE FROM api_keys WHERE token_hash=$1`), hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListAPIKeys returns keys for a tenant. An empty or "*" tenant returns every
// key. The result is always non-nil.
func (s *sqlStore) ListAPIKeys(ctx context.Context, tenant string) ([]APIKeyRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if tenant == "" || tenant == "*" {
		rows, err = s.db.QueryContext(ctx, s.rebind(
			`SELECT `+apiKeyCols+` FROM api_keys ORDER BY created_at`))
	} else {
		rows, err = s.db.QueryContext(ctx, s.rebind(
			`SELECT `+apiKeyCols+` FROM api_keys WHERE tenant_id=$1 ORDER BY created_at`), tenant)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]APIKeyRecord, 0)
	for rows.Next() {
		var rec APIKeyRecord
		if err := rows.Scan(&rec.TokenHash, &rec.TenantID, &rec.Scopes, &rec.Label, &rec.CreatedAt, &rec.ExpiresAt, &rec.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// TouchAPIKey records the most recent use of a key.
func (s *sqlStore) TouchAPIKey(ctx context.Context, hash, when string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE api_keys SET last_used_at=$1 WHERE token_hash=$2`), when, hash)
	return err
}
