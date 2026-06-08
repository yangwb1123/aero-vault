package s3compat

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// S3 GetObject/HeadObject honor ?versionId=<id>, returning that specific stored
// version (and its x-amz-version-id), not just the current one.
func TestS3GetObject_VersionId(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "obj")})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, nil)
	if err := svc.SetBucketVersioning(ctx, "default", "b", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "default", "b", "k.txt", strings.NewReader("version-one"), 11, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "default", "b", "k.txt", strings.NewReader("version-two"), 11, service.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewRouter(svc, nil))
	defer srv.Close()

	// Current GET returns v2.
	resp, body := do(t, "GET", srv.URL+"/b/k.txt", nil, nil)
	if resp.StatusCode != 200 || string(body) != "version-two" {
		t.Fatalf("current GET: status=%d body=%q", resp.StatusCode, body)
	}

	// ?versionId returns v1 + the version-id header.
	resp, body = do(t, "GET", srv.URL+"/b/k.txt?versionId="+v1.VersionID, nil, nil)
	if resp.StatusCode != 200 || string(body) != "version-one" {
		t.Fatalf("versionId GET: status=%d body=%q (want version-one)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("x-amz-version-id"); got != v1.VersionID {
		t.Fatalf("x-amz-version-id = %q, want %q", got, v1.VersionID)
	}

	// HEAD ?versionId returns headers for v1.
	resp, _ = do(t, "HEAD", srv.URL+"/b/k.txt?versionId="+v1.VersionID, nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("x-amz-version-id") != v1.VersionID {
		t.Fatalf("HEAD versionId: status=%d vid=%q", resp.StatusCode, resp.Header.Get("x-amz-version-id"))
	}
}
