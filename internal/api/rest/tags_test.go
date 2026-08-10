package rest

import (
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
)

func TestTagsCRUD(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h := NewHandler(service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{}), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Delete("/v1/files/*", h.deleteKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	base := srv.URL + "/v1/files/doc.txt"
	tagsURL := base + "/tags"

	// 1. Upload the object.
	resp, _ := req(t, "PUT", base, []byte("hello"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT object: status=%d want 201", resp.StatusCode)
	}

	// 2. GET tags on a freshly uploaded object → 200 with empty tag map.
	resp, body := req(t, "GET", tagsURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tags initial: status=%d want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"tags":{}`) {
		t.Fatalf("GET tags initial: body=%s want {\"tags\":{}}", body)
	}

	// 3. PUT tags.
	resp, _ = req(t, "PUT", tagsURL, []byte(`{"team":"eng","env":"prod"}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT tags: status=%d want 200", resp.StatusCode)
	}

	// 4. GET tags back — body must contain "eng".
	resp, body = req(t, "GET", tagsURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tags after PUT: status=%d want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "eng") {
		t.Fatalf("GET tags after PUT: body=%s want 'eng'", body)
	}

	// 5. DELETE tags → 204.
	resp, _ = req(t, "DELETE", tagsURL, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE tags: status=%d want 204", resp.StatusCode)
	}

	// 6. GET tags after DELETE → 200 with empty tag map.
	resp, body = req(t, "GET", tagsURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tags after DELETE: status=%d want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"tags":{}`) {
		t.Fatalf("GET tags after DELETE: body=%s want {\"tags\":{}}", body)
	}

	// 7. GET tags on a non-existent object → 404.
	resp, _ = req(t, "GET", srv.URL+"/v1/files/nonexistent.txt/tags", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET tags nonexistent: status=%d want 404", resp.StatusCode)
	}

	// 8. PUT tags with invalid JSON → 400.
	resp, _ = req(t, "PUT", tagsURL, []byte(`not-json`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT tags invalid JSON: status=%d want 400", resp.StatusCode)
	}
}
