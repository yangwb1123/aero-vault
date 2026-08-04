package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
)

const shareColumns = `id, tenant_id, bucket, object_key, name, token_hash,
 password_mac, allow_preview, allow_download, max_uses, use_count,
 expires_at, revoked_at, created_by, owner_id, created_at`

func (s *sqlStore) CreateShare(ctx context.Context, share access.Share) error {
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO shares (`+shareColumns+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`),
		share.ID, share.TenantID, share.Bucket, share.Key, share.Name, share.TokenHash,
		share.PasswordMAC, boolInt(share.AllowPreview), boolInt(share.AllowDownload),
		share.MaxUses, share.UseCount, accessTimeString(share.ExpiresAt),
		accessTimeString(share.RevokedAt), share.CreatedBy, share.OwnerID,
		accessTimeString(share.CreatedAt))
	return err
}

func (s *sqlStore) GetShare(ctx context.Context, tenant, id string) (access.Share, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+shareColumns+` FROM shares WHERE tenant_id=$1 AND id=$2`), tenant, id)
	return scanShare(row)
}

func (s *sqlStore) GetShareByTokenHash(ctx context.Context, hash string) (access.Share, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+shareColumns+` FROM shares WHERE token_hash=$1`), hash)
	return scanShare(row)
}

func scanShare(row rowScanner) (access.Share, error) {
	var share access.Share
	var preview, download int
	var expires, revoked, created string
	err := row.Scan(&share.ID, &share.TenantID, &share.Bucket, &share.Key, &share.Name,
		&share.TokenHash, &share.PasswordMAC, &preview, &download, &share.MaxUses,
		&share.UseCount, &expires, &revoked, &share.CreatedBy, &share.OwnerID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return access.Share{}, access.ErrNotFound
	}
	if err != nil {
		return access.Share{}, err
	}
	share.AllowPreview, share.AllowDownload = preview != 0, download != 0
	share.ExpiresAt, share.RevokedAt = accessTime(expires), accessTime(revoked)
	share.CreatedAt = accessTime(created)
	return share, nil
}

func (s *sqlStore) ListShares(ctx context.Context, tenant, bucket, key string) ([]access.Share, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT `+shareColumns+` FROM shares
		 WHERE tenant_id=$1 AND bucket=$2 AND object_key=$3 ORDER BY created_at DESC`),
		tenant, bucket, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.Share, 0)
	for rows.Next() {
		share, scanErr := scanShare(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, share)
	}
	return out, rows.Err()
}

func (s *sqlStore) RevokeShare(ctx context.Context, tenant, id, revokedAt string) error {
	result, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE shares SET revoked_at=$1 WHERE tenant_id=$2 AND id=$3 AND revoked_at=''`),
		revokedAt, tenant, id)
	return accessDeleteResult(result, err)
}

func (s *sqlStore) ConsumeShare(ctx context.Context, hash, now string) (access.Share, error) {
	result, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE shares SET use_count=use_count+1
		 WHERE token_hash=$1 AND revoked_at='' AND (expires_at='' OR expires_at>$2)
		 AND (max_uses=0 OR use_count<max_uses)`), hash, now)
	if err != nil {
		return access.Share{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return access.Share{}, err
	}
	if count == 0 {
		return access.Share{}, access.ErrShareExpired
	}
	return s.GetShareByTokenHash(ctx, hash)
}
