package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
)

func TestAdminRoutesUseIndependentRateLimiter(t *testing.T) {
	svc, repo, _ := setupTest(t)
	reg, err := auth.Parse("")
	if err != nil {
		t.Fatal(err)
	}
	adminRL := mw.NewRateLimiter(0.001, 1)
	router := NewRouter(
		svc, repo, nil, nil, nil, nil, reg, nil,
		false, nil, adminRL, 0, false,
	)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	resp, body := req(t, http.MethodGet, server.URL+"/admin/config", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first admin request: status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = req(t, http.MethodGet, server.URL+"/admin/config", nil, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second admin request: status=%d want 429 body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, http.MethodGet, server.URL+"/files", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ordinary route was affected by admin limiter: status=%d body=%s", resp.StatusCode, body)
	}
}
