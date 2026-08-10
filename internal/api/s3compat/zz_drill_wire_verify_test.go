package s3compat

// THROWAWAY verification test — deleted after run. Verifies wire behavior:
// (1) which method actually reaches restoreObject (?restore),
// (2) 503 shape on the four write paths,
// (3) read-path 200/404 shapes.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type throwawayAcct struct{ fail bool }

func (t *throwawayAcct) CheckQuota(ctx context.Context, tenant string, cur repository.TenantQuota, db, do int64) error {
	if t.fail {
		return fmt.Errorf("%w: tenant is not server-bound", service.ErrEntitlementUnavailable)
	}
	return nil
}

func (t *throwawayAcct) Apply(ctx context.Context, m service.UsageMutation) (repository.TenantQuota, error) {
	return repository.TenantQuota{}, nil
}

func TestZZWireVerify(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	acct := &throwawayAcct{fail: true}
	svc := service.NewFileService(store, repo, nil).
		WithUsageAccountant(acct).WithAuthorizer(allowAllProvider{})
	srv := httptest.NewServer(NewRouter(svc, nil, allowAllProvider{}))
	t.Cleanup(srv.Close)

	// Seed while healthy.
	acct.fail = false
	if resp, _ := do(t, "PUT", srv.URL+"/b/k.txt", []byte("hello"), nil); resp.StatusCode != 200 {
		t.Fatalf("seed PUT: %d", resp.StatusCode)
	}
	acct.fail = true

	// 1. POST ?restore — what really happens?
	resp, body := do(t, "POST", srv.URL+"/b/k.txt?restore", nil, nil)
	t.Logf("POST ?restore -> %d %s", resp.StatusCode, body)

	// 2. PUT ?restore — actual restore route.
	resp, body = do(t, "PUT", srv.URL+"/b/k.txt?restore", nil, nil)
	t.Logf("PUT ?restore  -> %d %s", resp.StatusCode, body)

	// 3. Four write paths, 503 shape.
	for name, req := range map[string][2]string{
		"PUT-object":   {"PUT", srv.URL + "/b/k2.txt"},
		"DELETE":       {"DELETE", srv.URL + "/b/k.txt"},
		"CreateBucket": {"PUT", srv.URL + "/b2"},
	} {
		r, b := do(t, req[0], req[1], []byte("x"), nil)
		t.Logf("%-12s -> %d %s", name, r.StatusCode, b)
	}

	// 4. Read paths while fail=true.
	r, b := do(t, "GET", srv.URL+"/b/k.txt", nil, nil)
	t.Logf("GET existing  -> %d %q", r.StatusCode, b)
	r, b = do(t, "GET", srv.URL+"/b/missing.txt", nil, nil)
	t.Logf("GET missing   -> %d %s", r.StatusCode, b)
	r, _ = do(t, "HEAD", srv.URL+"/b/k.txt", nil, nil)
	t.Logf("HEAD existing -> %d", r.StatusCode)
	r, b = do(t, "GET", srv.URL+"/b?list-type=2", nil, nil)
	t.Logf("ListObjectsV2 -> %d %s", r.StatusCode, b)
	r, _ = do(t, "HEAD", srv.URL+"/b", nil, nil)
	t.Logf("HeadBucket    -> %d", r.StatusCode)
	r2, b2 := do(t, "HEAD", srv.URL+"/nobucket", nil, nil)
	t.Logf("HeadBucket missing -> %d body=%q", r2.StatusCode, b2)
}
