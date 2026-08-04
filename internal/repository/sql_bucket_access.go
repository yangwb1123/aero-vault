package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ── CORS ────────────────────────────────────────────────────────────────────

func (s *sqlStore) GetBucketCORS(ctx context.Context, tenant, bucket string) ([]CORSRule, error) {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return nil, err
	}
	var corsRaw string
	err := s.db.QueryRowContext(ctx, s.rebind(`SELECT cors_rules FROM buckets WHERE tenant_id=$1 AND name=$2`), tenant, bucket).Scan(&corsRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if corsRaw == "" {
		return []CORSRule{}, nil
	}
	var rules []CORSRule
	if err := json.Unmarshal([]byte(corsRaw), &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *sqlStore) SetBucketCORS(ctx context.Context, tenant, bucket string, rules []CORSRule) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	data, _ := json.Marshal(rules)
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET cors_rules=$1 WHERE tenant_id=$2 AND name=$3`), string(data), tenant, bucket)
	return err
}

func (s *sqlStore) DeleteBucketCORS(ctx context.Context, tenant, bucket string) error {
	tenant = defaultTenant(tenant)
	_, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE buckets SET cors_rules=$1 WHERE tenant_id=$2 AND name=$3`,
	), "[]", tenant, bucket)
	return err
}

// ── Logging ─────────────────────────────────────────────────────────────────

func (s *sqlStore) GetBucketLogging(ctx context.Context, tenant, bucket string) (LoggingConfig, error) {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return LoggingConfig{}, err
	}
	var target, prefix string
	err := s.db.QueryRowContext(ctx, s.rebind(`SELECT logging_target, logging_prefix FROM buckets WHERE tenant_id=$1 AND name=$2`), tenant, bucket).Scan(&target, &prefix)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LoggingConfig{}, nil
		}
		return LoggingConfig{}, err
	}
	return LoggingConfig{Enabled: target != "", Target: target, Prefix: prefix}, nil
}

func (s *sqlStore) SetBucketLogging(ctx context.Context, tenant, bucket, targetBucket, targetPrefix string) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET logging_target=$1, logging_prefix=$2 WHERE tenant_id=$3 AND name=$4`), targetBucket, targetPrefix, tenant, bucket)
	return err
}

func (s *sqlStore) DeleteBucketLogging(ctx context.Context, tenant, bucket string) error {
	tenant = defaultTenant(tenant)
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET logging_target='', logging_prefix='' WHERE tenant_id=$1 AND name=$2`), tenant, bucket)
	return err
}

func (s *sqlStore) WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error {
	_ = tenant
	_ = sourceBucket
	_ = method
	_ = key
	_ = status
	_ = latencyMs
	_ = userAgent
	return nil
}

// ── Notifications ───────────────────────────────────────────────────────────

func (s *sqlStore) GetBucketNotifications(ctx context.Context, tenant, bucket string) ([]NotificationRule, error) {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return nil, err
	}
	var notifRaw string
	err := s.db.QueryRowContext(ctx, s.rebind(`SELECT notification_rules FROM buckets WHERE tenant_id=$1 AND name=$2`), tenant, bucket).Scan(&notifRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if notifRaw == "" || notifRaw == "[]" {
		return nil, nil
	}
	var rules []NotificationRule
	_ = json.Unmarshal([]byte(notifRaw), &rules)
	return rules, nil
}

func (s *sqlStore) SetBucketNotifications(ctx context.Context, tenant, bucket string, rules []NotificationRule) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	data, _ := json.Marshal(rules)
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET notification_rules=$1 WHERE tenant_id=$2 AND name=$3`), string(data), tenant, bucket)
	return err
}

func (s *sqlStore) DeleteBucketNotifications(ctx context.Context, tenant, bucket string) error {
	tenant = defaultTenant(tenant)
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET notification_rules='[]' WHERE tenant_id=$1 AND name=$2`), tenant, bucket)
	return err
}
