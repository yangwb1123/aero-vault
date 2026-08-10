package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// allowAllProvider is the access.Authorizer test double used by the admin
// delete suite: no authorizer (or a nil manager) keeps the CI baseline so
// these behavioral tests exercise the admin endpoint, not the fail-closed
// delete gate (covered by dedicated tests).
type allowAllProvider struct{}

func (allowAllProvider) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: true, Reason: "test_allow_all"}, nil
}

// TestRESTDeleteDenied_403_NoOutbox — AC-2/AC-3 (T7/T9): at the HTTP boundary a
// fail-closed denied delete answers 403 AccessDenied with the denial reason in
// message and leaves the outbox table untouched. Harness mirrors
// enterprise_access_test.go (manager + real auth middleware), but with a
// DefaultDeny policy so alice (read+write scopes, no ACL grant) is denied.
func TestRESTDeleteDenied_403_NoOutbox(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "deny.db"))
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
	accessStore, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	manager, err := access.NewManager(accessStore, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultDeny,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, slog.Default()).
		WithAuthorizer(manager)
	reg, err := auth.Parse("alice:default:read+write,operator:*:admin")
	if err != nil {
		t.Fatal(err)
	}
	v1 := NewRouter(svc, repo, nil, nil, nil, nil, reg, slog.Default(), false,
		nil, nil, 0, false,
		func(h *Handler) { h.WithAccessManager(manager, "") })
	root := chi.NewRouter()
	root.Mount("/v1", v1)
	ts := httptest.NewServer(reg.Middleware()(root))
	t.Cleanup(ts.Close)

	url := ts.URL + "/v1/files/k.txt"
	opResp, opBody := req(t, http.MethodPut, url, []byte("payload"), map[string]string{
		"Authorization": "Bearer operator",
	})
	if opResp.StatusCode != http.StatusCreated {
		t.Fatalf("operator PUT status=%d body=%s", opResp.StatusCode, opBody)
	}
	obj, err := repo.GetObject(ctx, "default", "default", "k.txt")
	if err != nil {
		t.Fatal(err)
	}

	resp, body := req(t, http.MethodDelete, url, nil, map[string]string{
		"Authorization": "Bearer alice",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("alice DELETE status=%d body=%s; want 403", resp.StatusCode, body)
	}
	var envelope errorBody
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("403 body is not the REST error envelope: %v (%s)", err, body)
	}
	if envelope.Error.Code != "AccessDenied" {
		t.Errorf("code=%q; want AccessDenied", envelope.Error.Code)
	}
	if !strings.Contains(envelope.Error.Message, "default_deny") {
		t.Errorf("message=%q; want denial reason default_deny", envelope.Error.Message)
	}
	for _, eventType := range []repository.OutboxEventType{
		repository.EventTypeFileDeleted11, repository.EventTypeFileNotify11,
	} {
		has, hasErr := repo.HasEventOutboxFact(ctx, obj.ID, eventType)
		if hasErr != nil || has {
			t.Fatalf("denied REST delete wrote outbox fact %s: has=%v err=%v", eventType, has, hasErr)
		}
	}
	getResp, _ := req(t, http.MethodGet, url, nil, map[string]string{
		"Authorization": "Bearer operator",
	})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("object must survive denied delete, GET status=%d", getResp.StatusCode)
	}
}
