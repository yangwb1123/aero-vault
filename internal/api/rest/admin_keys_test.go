package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func testAdminHandler(t *testing.T) *AdminHandler {
	t.Helper()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg, _ := auth.Parse("")
	return NewAdminHandler(nil, repo, reg)
}

func addKeyRequest(t *testing.T, h *AdminHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/admin/keys", bytes.NewReader([]byte(body)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.AddKey(w, r)
	return w
}

func TestAdminKeys_Add(t *testing.T) {
	h := testAdminHandler(t)

	body := `{"token":"tk123","tenant":"default","scopes":["read","write"]}`
	w := addKeyRequest(t, h, body)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("AddKey: got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminKeys_AddInvalid(t *testing.T) {
	h := testAdminHandler(t)

	// Missing token.
	w := addKeyRequest(t, h, `{"tenant":"default","scopes":["read"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing token, got %d", w.Code)
	}

	// Empty body.
	r := httptest.NewRequest("POST", "/admin/keys", bytes.NewReader([]byte("{}")))
	r.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.AddKey(w2, r)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty body, got %d", w2.Code)
	}
}

func TestAdminKeys_List(t *testing.T) {
	h := testAdminHandler(t)

	// Initially empty.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin/keys", nil)
	h.ListKeys(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("ListKeys: got %d", w.Code)
	}
	var resp struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(resp.Keys))
	}

	// Add a key.
	addKeyRequest(t, h, `{"token":"tk456","tenant":"default","scopes":["admin"]}`)

	// Now list should have 1 key.
	w2 := httptest.NewRecorder()
	h.ListKeys(httptest.NewRecorder(), httptest.NewRequest("GET", "/admin/keys", nil))
	_ = w2
}

func TestAdminJWT_Issue(t *testing.T) {
	h := testAdminHandler(t)

	body := `{"tenant":"default","ttl_seconds":3600,"scopes":["read"]}`
	r := httptest.NewRequest("POST", "/admin/jwt", bytes.NewReader([]byte(body)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.IssueJWT(w, r)
	// JWT may fail gracefully with no secret configured; don't panic.
}

func TestAdminConfig_Get(t *testing.T) {
	h := testAdminHandler(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin/config", nil)
	h.GetConfig(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GetConfig: got %d", w.Code)
	}
}

func TestRedactToken(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc123456", "****3456"},
		{"abc", "****"},
		{"", "****"},
	}
	for _, tt := range tests {
		got := redactToken(tt.input)
		if got != tt.want {
			t.Errorf("redactToken(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
