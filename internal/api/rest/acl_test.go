package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// newAuthRESTTest wires the auth middleware (with a single read+write key and
// anonymous-public-read enabled) around the object routes.
func newAuthRESTTest(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "acl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	h := NewHandler(service.NewFileService(store, repo, nil), nil)

	reg, err := auth.Parse("k1:default:read+write")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	reg.WithAnonymousPublicRead(true)

	r := chi.NewRouter()
	r.Use(reg.Middleware())
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv, "Bearer k1"
}

func TestObjectACLAnonymousEnforcement(t *testing.T) {
	s, tok := newAuthRESTTest(t)
	u := s.URL + "/v1/files/report.txt"
	authH := map[string]string{"Authorization": tok}

	// Upload (authenticated).
	if resp, _ := req(t, "PUT", u, []byte("secret data"), authH); resp.StatusCode != http.StatusCreated {
		t.Fatalf("authed PUT: %d", resp.StatusCode)
	}
	// Anonymous GET of a private object → 403.
	if resp, _ := req(t, "GET", u, nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anon GET private: status=%d want 403", resp.StatusCode)
	}
	// Authenticated GET works.
	if resp, body := req(t, "GET", u, nil, authH); resp.StatusCode != 200 || string(body) != "secret data" {
		t.Fatalf("authed GET: status=%d body=%q", resp.StatusCode, body)
	}
	// Make it public-read.
	if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != 200 {
		t.Fatalf("set acl: %d", resp.StatusCode)
	}
	// Anonymous GET now succeeds.
	if resp, body := req(t, "GET", u, nil, nil); resp.StatusCode != 200 || string(body) != "secret data" {
		t.Fatalf("anon GET public: status=%d body=%q", resp.StatusCode, body)
	}
	for _, suffix := range []string{"/tags", "/versions", "/acl", "/metadata"} {
		if resp, _ := req(t, "GET", u+suffix, nil, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anon GET public subresource %s: status=%d want 401", suffix, resp.StatusCode)
		}
	}
	// Anonymous GET of a different private object still 403.
	req(t, "PUT", s.URL+"/v1/files/other.txt", []byte("x"), authH)
	if resp, _ := req(t, "GET", s.URL+"/v1/files/other.txt", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anon GET other private: status=%d want 403", resp.StatusCode)
	}
	// Anonymous write is always rejected (401, never reaches handler).
	if resp, _ := req(t, "PUT", s.URL+"/v1/files/hack.txt", []byte("x"), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon PUT: status=%d want 401", resp.StatusCode)
	}
}

func TestObjectACLGetSet(t *testing.T) {
	s := newRESTTest(t) // no auth → open mode
	u := s.URL + "/v1/files/doc.txt"
	req(t, "PUT", u, []byte("hi"), nil)

	// Default ACL is private.
	_, body := req(t, "GET", u+"/acl", nil, nil)
	if !strings.Contains(string(body), `"private"`) {
		t.Fatalf("default acl: %s", body)
	}
	// Set + read back.
	if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), nil); resp.StatusCode != 200 {
		t.Fatalf("set acl status %d", resp.StatusCode)
	}
	_, body = req(t, "GET", u+"/acl", nil, nil)
	if !strings.Contains(string(body), `"public-read"`) {
		t.Fatalf("acl after set: %s", body)
	}
	// Invalid ACL → 400.
	if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"bogus"}`), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid acl: status=%d want 400", resp.StatusCode)
	}
}
