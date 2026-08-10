package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
)

const accessACLColumns = `id, tenant_id, bucket, resource_key, resource_kind,
 principal_type, principal_id, action, effect, inherit_acl, created_by, created_at`

func (s *sqlStore) PutACLEntry(ctx context.Context, entry access.ACLEntry) error {
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO resource_acls (`+accessACLColumns+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT (id) DO UPDATE SET principal_type=excluded.principal_type,
		 principal_id=excluded.principal_id, action=excluded.action,
		 effect=excluded.effect, inherit_acl=excluded.inherit_acl`),
		entry.ID, entry.TenantID, entry.Bucket, entry.Key, string(entry.ResourceKind),
		string(entry.PrincipalType), entry.PrincipalID, string(entry.Action), string(entry.Effect),
		boolInt(entry.Inherit), entry.CreatedBy, accessTimeString(entry.CreatedAt))
	return err
}

func (s *sqlStore) DeleteACLEntry(ctx context.Context, tenant, id string) error {
	result, err := s.db.ExecContext(ctx, s.rebind(
		`DELETE FROM resource_acls WHERE tenant_id=$1 AND id=$2`), tenant, id)
	return accessDeleteResult(result, err)
}

func (s *sqlStore) GetACLEntry(ctx context.Context, tenant, id string) (access.ACLEntry, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+accessACLColumns+` FROM resource_acls WHERE tenant_id=$1 AND id=$2`),
		tenant, id)
	entry, err := scanACLEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return access.ACLEntry{}, access.ErrNotFound
	}
	return entry, err
}

func (s *sqlStore) ListResourceACL(
	ctx context.Context,
	tenant, bucket, key string,
	kind access.ResourceKind,
) ([]access.ACLEntry, error) {
	return s.queryACL(ctx,
		`SELECT `+accessACLColumns+` FROM resource_acls
		 WHERE tenant_id=$1 AND bucket=$2 AND resource_key=$3 AND resource_kind=$4
		 ORDER BY created_at, id`, tenant, bucket, key, string(kind))
}

func (s *sqlStore) ListApplicableACL(ctx context.Context, tenant, bucket, key string) ([]access.ACLEntry, error) {
	// Literal prefix comparison instead of LIKE: a folder key containing
	// '%' or '_' (e.g. report_2026/) must not act as a SQL wildcard and
	// widen the ACL to sibling keys (reportX2026/...). folder keys always
	// end in '/' (normalizeACLResource), so the length-bound comparison
	// preserves the slash boundary; substr/length are portable SQLite/PG.
	return s.queryACL(ctx,
		`SELECT `+accessACLColumns+` FROM resource_acls
		 WHERE tenant_id=$1 AND bucket=$2 AND (
		   resource_kind='bucket'
		   OR (resource_kind='object' AND resource_key=$3)
		   OR (resource_kind='folder' AND (resource_key=$4 OR (inherit_acl=1 AND substr($5, 1, length(resource_key)) = resource_key)))
		 ) ORDER BY LENGTH(resource_key) DESC, created_at, id`,
		tenant, bucket, key, key, key)
}

func (s *sqlStore) queryACL(ctx context.Context, query string, args ...any) ([]access.ACLEntry, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.ACLEntry, 0)
	for rows.Next() {
		entry, scanErr := scanACLEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func scanACLEntry(row rowScanner) (access.ACLEntry, error) {
	var entry access.ACLEntry
	var kind, principalType, action, effect, created string
	var inherit int
	err := row.Scan(&entry.ID, &entry.TenantID, &entry.Bucket, &entry.Key, &kind,
		&principalType, &entry.PrincipalID, &action, &effect, &inherit,
		&entry.CreatedBy, &created)
	entry.ResourceKind = access.ResourceKind(kind)
	entry.PrincipalType = access.PrincipalType(principalType)
	entry.Action, entry.Effect = access.Action(action), access.Effect(effect)
	entry.Inherit, entry.CreatedAt = inherit != 0, accessTime(created)
	return entry, err
}
