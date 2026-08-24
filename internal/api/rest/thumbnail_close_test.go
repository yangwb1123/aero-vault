package rest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func TestThumbnailCloseVerificationFailureIsGoneAndNotCached(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "close.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	realStore, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	store := &countingStore{Storage: realStore}
	svc := service.NewFileService(store, repo, nil).
		WithDeleteFailOpen(true).
		WithReadVerification(service.ReadVerificationConfig{Enabled: true})
	h := NewHandler(svc, nil).WithThumbnailCache(thumbnail.NewCache(1<<20, 0))
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = repo.Close()
	})

	url := srv.URL + "/v1/files/verified.png"
	data := pngBytes(t, 64, 64)
	if resp, _ := req(t, "PUT", url, data, map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if err := repo.SetObjectMetaKey(ctx, "default", "default", "verified.png", "_aero_content_md5", strings.Repeat("0", 32)); err != nil {
		t.Fatalf("tamper verification metadata: %v", err)
	}

	for i := 0; i < 2; i++ {
		resp, body := req(t, "GET", url+"/thumbnail?w=32&h=32", nil, nil)
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("request %d status = %d, want 410 (body=%q)", i+1, resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte(`"code":"ObjectCorrupt"`)) {
			t.Fatalf("request %d body = %s, want ObjectCorrupt", i+1, body)
		}
		if got := resp.Header.Get("ETag"); got != "" {
			t.Fatalf("request %d emitted success ETag %q", i+1, got)
		}
		if got := resp.Header.Get("Cache-Control"); got != "" {
			t.Fatalf("request %d emitted success Cache-Control %q", i+1, got)
		}
	}
	if got := store.gets.Load(); got != 2 {
		t.Fatalf("storage Get count = %d, want 2 (failed result must not be cached)", got)
	}
	if h.thumbnailCache.Len() != 0 || h.thumbnailCache.Bytes() != 0 {
		t.Fatalf("failed thumbnail populated cache: len=%d bytes=%d", h.thumbnailCache.Len(), h.thumbnailCache.Bytes())
	}
}
