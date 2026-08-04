package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
)

const publicAssetColumns = `id, tenant_id, bucket, object_key, slug,
 cache_control, published_by, owner_id, published_at`

func (s *sqlStore) PutPublicAsset(ctx context.Context, asset access.PublicAsset) error {
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO public_assets (`+publicAssetColumns+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET object_key=excluded.object_key,
		 slug=excluded.slug, cache_control=excluded.cache_control,
		 published_by=excluded.published_by, owner_id=excluded.owner_id,
		 published_at=excluded.published_at`),
		asset.ID, asset.TenantID, asset.Bucket, asset.Key, asset.Slug,
		asset.CacheControl, asset.PublishedBy, asset.OwnerID,
		accessTimeString(asset.PublishedAt))
	return err
}

func (s *sqlStore) GetPublicAsset(ctx context.Context, slug string) (access.PublicAsset, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+publicAssetColumns+` FROM public_assets WHERE slug=$1`), slug)
	return scanPublicAsset(row)
}

func scanPublicAsset(row rowScanner) (access.PublicAsset, error) {
	var asset access.PublicAsset
	var published string
	err := row.Scan(&asset.ID, &asset.TenantID, &asset.Bucket, &asset.Key,
		&asset.Slug, &asset.CacheControl, &asset.PublishedBy, &asset.OwnerID, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return access.PublicAsset{}, access.ErrNotFound
	}
	if err != nil {
		return access.PublicAsset{}, err
	}
	asset.PublishedAt = accessTime(published)
	return asset, nil
}

func (s *sqlStore) ListPublicAssets(ctx context.Context, tenant string) ([]access.PublicAsset, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT `+publicAssetColumns+` FROM public_assets
		 WHERE tenant_id=$1 ORDER BY published_at DESC, id`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.PublicAsset, 0)
	for rows.Next() {
		asset, scanErr := scanPublicAsset(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, asset)
	}
	return out, rows.Err()
}

func (s *sqlStore) DeletePublicAsset(ctx context.Context, tenant, slug string) error {
	result, err := s.db.ExecContext(ctx, s.rebind(
		`DELETE FROM public_assets WHERE tenant_id=$1 AND slug=$2`), tenant, slug)
	return accessDeleteResult(result, err)
}
