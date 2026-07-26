package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ── Bucket CRUD ─────────────────────────────────────────────────────────────

func (s *sqlStore) CreateBucket(ctx context.Context, tenant, bucket string) error {
	tenant = defaultTenant(tenant)
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO buckets (tenant_id, name) VALUES ($1, $2) ON CONFLICT (tenant_id, name) DO NOTHING`
	} else {
		q = `INSERT OR IGNORE INTO buckets (tenant_id, name) VALUES ($1, $2)`
	}
	_, err := s.db.ExecContext(ctx, s.rebind(q), tenant, bucket)
	return err
}

func (s *sqlStore) BucketExists(ctx context.Context, tenant, bucket string) (bool, error) {
	tenant = defaultTenant(tenant)
	var count int
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT COUNT(1) FROM buckets WHERE tenant_id = $1 AND name = $2`), tenant, bucket)
	if err := row.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *sqlStore) ListBuckets(ctx context.Context, tenant string) ([]string, error) {
	tenant = defaultTenant(tenant)
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT name FROM buckets WHERE tenant_id = $1`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *sqlStore) BucketStats(ctx context.Context, tenant, bucket string) (BucketStats, error) {
	tenant = defaultTenant(tenant)
	var bs BucketStats
	bs.Bucket = bucket
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT COUNT(1), COALESCE(SUM(size), 0) FROM objects WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NULL`),
		tenant, bucket)
	if err := row.Scan(&bs.ObjectCount, &bs.TotalSize); err != nil {
		return BucketStats{}, err
	}
	return bs, nil
}

func (s *sqlStore) DeleteBucket(ctx context.Context, tenant, bucket string) error {
	tenant = defaultTenant(tenant)
	exists, err := s.BucketExists(ctx, tenant, bucket)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Delete all multipart uploads and their parts.
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM multipart_parts WHERE upload_id IN (SELECT id FROM multipart_uploads WHERE tenant_id=$1 AND bucket=$2)`), tenant, bucket); err != nil {
		_ = err // best-effort (table may be absent in older schemas)
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM multipart_uploads WHERE tenant_id=$1 AND bucket=$2`), tenant, bucket); err != nil {
		_ = err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM chunks WHERE object_id IN (SELECT id FROM objects WHERE tenant_id=$1 AND bucket=$2)`), tenant, bucket); err != nil {
		if s.dialect == dialectSQLite {
			_ = err
		}
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM objects WHERE tenant_id=$1 AND bucket=$2`), tenant, bucket); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM events WHERE tenant_id=$1 AND bucket=$2`), tenant, bucket); err != nil {
		if s.dialect == dialectSQLite {
			_ = err
		}
	}
	res, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM buckets WHERE tenant_id=$1 AND name=$2`), tenant, bucket)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ── Bucket Config ───────────────────────────────────────────────────────────

func (s *sqlStore) GetBucketConfig(ctx context.Context, tenant, bucket string) (BucketConfig, error) {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT tenant_id, name, versioning, object_lock_seconds, expire_after_days, expire_action, noncurrent_days, noncurrent_count, acl, policy, cors_rules, logging_target, logging_prefix, notification_rules, sse_algorithm, sse_kms_key_id FROM buckets WHERE tenant_id=$1 AND name=$2`), tenant, bucket)
	var cfg BucketConfig
	var versioning sql.NullBool
	var acl, policy, corsRaw, logTarget, logPrefix, notifRaw sql.NullString
	if err := row.Scan(&cfg.TenantID, &cfg.Name, &versioning, &cfg.ObjectLockSeconds, &cfg.ExpireAfterDays, &cfg.ExpireAction, &cfg.NoncurrentDays, &cfg.NoncurrentCount, &acl, &policy, &corsRaw, &logTarget, &logPrefix, &notifRaw, &cfg.SSEAlgorithm, &cfg.SSEKMSKeyId); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BucketConfig{TenantID: tenant, Name: bucket}, nil
		}
		return BucketConfig{}, err
	}
	cfg.Versioning = versioning.Bool
	cfg.ACL = acl.String
	cfg.Policy = policy.String
	if corsRaw.Valid && corsRaw.String != "" {
		_ = json.Unmarshal([]byte(corsRaw.String), &cfg.CORSRules)
	}
	cfg.LoggingTarget = logTarget.String
	cfg.LoggingPrefix = logPrefix.String
	if notifRaw.Valid && notifRaw.String != "" {
		_ = json.Unmarshal([]byte(notifRaw.String), &cfg.NotificationRules)
	}
	return cfg, nil
}

func (s *sqlStore) SetBucketEncryption(ctx context.Context, tenant, bucket, algorithm, kmsKeyID string) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET sse_algorithm=$1, sse_kms_key_id=$2 WHERE tenant_id=$3 AND name=$4`), algorithm, kmsKeyID, tenant, bucket)
	return err
}

func (s *sqlStore) DeleteBucketEncryption(ctx context.Context, tenant, bucket string) error {
	tenant = defaultTenant(tenant)
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET sse_algorithm='', sse_kms_key_id='' WHERE tenant_id=$1 AND name=$2`), tenant, bucket)
	return err
}

func (s *sqlStore) SetBucketPolicy(ctx context.Context, tenant, bucket, policy string) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET policy=$1 WHERE tenant_id=$2 AND name=$3`), policy, tenant, bucket)
	return err
}

func (s *sqlStore) SetBucketACL(ctx context.Context, tenant, bucket, acl string) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET acl=$1 WHERE tenant_id=$2 AND name=$3`), acl, tenant, bucket)
	return err
}

func (s *sqlStore) SetBucketVersioning(ctx context.Context, tenant, bucket string, enabled bool) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET versioning=$1 WHERE tenant_id=$2 AND name=$3`), v, tenant, bucket)
	return err
}

func (s *sqlStore) SetBucketObjectLock(ctx context.Context, tenant, bucket string, seconds int) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET object_lock_seconds=$1 WHERE tenant_id=$2 AND name=$3`), seconds, tenant, bucket)
	return err
}
