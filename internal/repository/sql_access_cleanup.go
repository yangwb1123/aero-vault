package repository

import (
	"context"
	"database/sql"
)

type accessExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func deleteObjectCapabilities(
	ctx context.Context, store *sqlStore, exec accessExecer, tenant, bucket, key string,
) error {
	queries := []string{
		`DELETE FROM shares WHERE tenant_id=$1 AND bucket=$2 AND object_key=$3`,
		`DELETE FROM public_assets WHERE tenant_id=$1 AND bucket=$2 AND object_key=$3`,
	}
	for _, query := range queries {
		if _, err := exec.ExecContext(ctx, store.rebind(query), tenant, bucket, key); err != nil {
			return err
		}
	}
	return nil
}

func deleteObjectAccessState(
	ctx context.Context, store *sqlStore, exec accessExecer, tenant, bucket, key string,
) error {
	if err := deleteObjectCapabilities(ctx, store, exec, tenant, bucket, key); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, store.rebind(
		`DELETE FROM resource_acls
		 WHERE tenant_id=$1 AND bucket=$2 AND resource_key=$3 AND resource_kind='object'`,
	), tenant, bucket, key)
	return err
}

func deleteBucketAccessState(
	ctx context.Context, store *sqlStore, exec accessExecer, tenant, bucket string,
) error {
	queries := []string{
		`DELETE FROM shares WHERE tenant_id=$1 AND bucket=$2`,
		`DELETE FROM public_assets WHERE tenant_id=$1 AND bucket=$2`,
		`DELETE FROM resource_acls WHERE tenant_id=$1 AND bucket=$2`,
	}
	for _, query := range queries {
		if _, err := exec.ExecContext(ctx, store.rebind(query), tenant, bucket); err != nil {
			return err
		}
	}
	return nil
}
