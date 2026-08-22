package repository

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWriteAccessLogPersistsRequestDetails(t *testing.T) {
	ctx := context.Background()
	repo, err := Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "access-log.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	if err := repo.WriteAccessLog(ctx, "tenant-a", "source", "GET", "docs/a.txt", "200", "7", "test-agent"); err != nil {
		t.Fatalf("WriteAccessLog: %v", err)
	}

	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo type %T, want *sqlStore", repo)
	}
	var tenant, bucket, method, key, status, latency, userAgent string
	err = store.db.QueryRowContext(ctx, `
		SELECT tenant_id, source_bucket, method, object_key, status, latency_ms, user_agent
		FROM bucket_access_logs`).Scan(&tenant, &bucket, &method, &key, &status, &latency, &userAgent)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	if tenant != "tenant-a" || bucket != "source" || method != "GET" || key != "docs/a.txt" || status != "200" || latency != "7" || userAgent != "test-agent" {
		t.Fatalf("access log = %q/%q/%q/%q/%q/%q/%q", tenant, bucket, method, key, status, latency, userAgent)
	}
}
