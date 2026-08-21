package webdav_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/api/webdav"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestWebDAVDeleteWithoutProviderReturns403AndPreservesObject(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "webdav-authz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.Put(ctx, "", "", "protected.txt", strings.NewReader("body"), 4, service.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mw.Tenant(webdav.Handler("/webdav", svc, nil)))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/webdav/protected.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET before delete status=%d; want 200", resp.StatusCode)
	}
	resp, err = http.DefaultClient.Do(mustRequest(http.MethodDelete, ts.URL+"/webdav/protected.txt"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("DELETE without provider status=%d; want 403", resp.StatusCode)
	}
	if _, err := svc.Stat(ctx, "", "", "protected.txt"); err != nil {
		t.Fatalf("object after denied WebDAV delete: %v", err)
	}
}

func mustRequest(method, target string) *http.Request {
	return mustRequestWithBody(method, target)
}

func mustRequestWithBody(method, target string) *http.Request {
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		panic(err)
	}
	return req
}
