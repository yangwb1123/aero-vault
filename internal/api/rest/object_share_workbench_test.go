package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type workbenchShareResponse struct {
	Share access.Share `json:"share"`
	Token string       `json:"token"`
	URL   string       `json:"url"`
}

func workbenchShareRequest(key string) []byte {
	body, _ := json.Marshal(map[string]any{
		"bucket": "default", "key": key, "allow_preview": true, "allow_download": true,
		"ttl_seconds": 3600,
	})
	return body
}

func createWorkbenchShare(t *testing.T, server *httptest.Server, authorization, key string) workbenchShareResponse {
	t.Helper()
	response, body := req(t, http.MethodPost, server.URL+"/v1/shares", workbenchShareRequest(key),
		map[string]string{"Authorization": authorization, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("share status=%d body=%s", response.StatusCode, body)
	}
	var result workbenchShareResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestObjectShareWorkbenchReadDoesNotGrantSharing(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, operator, "operator-owned.txt")
	response, body := req(t, http.MethodGet, server.URL+"/v1/files/operator-owned.txt", nil,
		map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read status=%d body=%s", response.StatusCode, body)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		target := server.URL + "/v1/shares"
		if method == http.MethodGet {
			target += "?bucket=default&key=operator-owned.txt"
		}
		response, body = req(t, method, target, workbenchShareRequest("operator-owned.txt"),
			map[string]string{"Authorization": alice, "Content-Type": "application/json"})
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s share status=%d body=%s", method, response.StatusCode, body)
		}
	}
}

func TestObjectShareWorkbenchSpecialKeyLifecycle(t *testing.T) {
	server, alice, _, _ := newEnterpriseRESTTest(t)
	key := " docs/100% report.txt "
	// Match the Web client's segment encoding; keep path separators literal.
	encodedKey := strings.ReplaceAll(url.PathEscape(key), "%2F", "/")
	response, body := req(t, http.MethodPut, server.URL+"/v1/files/"+encodedKey, []byte("content"),
		map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.StatusCode, body)
	}
	share := createWorkbenchShare(t, server, alice, key)
	if share.Share.Key != key || share.Token == "" || share.URL == "" {
		t.Fatalf("share target/URL lost: %+v", share.Share)
	}
	response, body = req(t, http.MethodGet, share.URL, nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "content" {
		t.Fatalf("share read status=%d body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, server.URL+"/v1/shares?bucket=default&key="+url.QueryEscape(key), nil,
		map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), share.Share.ID) || strings.Contains(string(body), share.Token) {
		t.Fatalf("list missing share or exposing token: status=%d body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodDelete, server.URL+"/v1/shares/"+share.Share.ID, nil,
		map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, share.URL, nil, nil)
	if response.StatusCode != http.StatusGone {
		t.Fatalf("revoked share status=%d body=%s", response.StatusCode, body)
	}
}

func TestObjectShareWorkbenchExplicitDenyAndDeletedTarget(t *testing.T) {
	server, alice, operator, repo := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, alice, "owner.txt")
	uploadListingFile(t, server, alice, "deleted.txt")
	response, body := req(t, http.MethodPut, server.URL+"/v1/access/acl", []byte(`{
		"bucket":"default","key":"owner.txt","resource_kind":"object",
		"principal_type":"everyone","actions":["object:share"],"effect":"deny"
	}`), map[string]string{"Authorization": operator, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("share ACL status=%d body=%s", response.StatusCode, body)
	}
	if err := repo.SoftDeleteObject(context.Background(), "default", "default", "deleted.txt"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		key  string
		want int
	}{{"owner.txt", http.StatusForbidden}, {"deleted.txt", http.StatusNotFound}} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			target := server.URL + "/v1/shares"
			if method == http.MethodGet {
				target += "?bucket=default&key=" + test.key
			}
			response, body = req(t, method, target, workbenchShareRequest(test.key),
				map[string]string{"Authorization": alice, "Content-Type": "application/json"})
			if response.StatusCode != test.want {
				t.Fatalf("%s %s share status=%d want=%d body=%s", method, test.key, response.StatusCode, test.want, body)
			}
		}
	}
}

func TestObjectShareWorkbenchTenantBoundaryAndSuspension(t *testing.T) {
	server, alice, _, repo := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, alice, "tenant.txt")
	share := createWorkbenchShare(t, server, alice, "tenant.txt")
	assertWorkbenchTenantDenial(t, server, alice, share.Share.ID, "other")
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{TenantID: "default", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	assertWorkbenchTenantDenial(t, server, alice, share.Share.ID, "default")
	response, body := req(t, http.MethodGet, share.URL, nil, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("suspended public share status=%d body=%s", response.StatusCode, body)
	}
}

func assertWorkbenchTenantDenial(t *testing.T, server *httptest.Server, authorization, id, tenant string) {
	t.Helper()
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/v1/shares?bucket=default&key=tenant.txt"},
		{http.MethodPost, "/v1/shares"}, {http.MethodDelete, "/v1/shares/" + id},
	} {
		response, body := req(t, test.method, server.URL+test.path, workbenchShareRequest("tenant.txt"),
			map[string]string{"Authorization": authorization, "Content-Type": "application/json", "X-Aero-Tenant": tenant})
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("tenant=%s %s share status=%d body=%s", tenant, test.method, response.StatusCode, body)
		}
	}
}
