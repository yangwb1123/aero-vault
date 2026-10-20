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

func workbenchACLBody(key string, kind access.ResourceKind, effect access.Effect, actions ...string) []byte {
	body, _ := json.Marshal(map[string]any{
		"bucket": "default", "key": key, "resource_kind": kind, "principal_type": "everyone",
		"actions": actions, "effect": effect, "inherit": kind == access.ResourceFolder,
	})
	return body
}

func putWorkbenchACL(t *testing.T, server *httptest.Server, auth, key string, kind access.ResourceKind, effect access.Effect, actions ...string) []access.ACLEntry {
	t.Helper()
	response, body := req(t, http.MethodPut, server.URL+"/v1/access/acl", workbenchACLBody(key, kind, effect, actions...),
		map[string]string{"Authorization": auth, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("put ACL status=%d body=%s", response.StatusCode, body)
	}
	var result struct {
		Entries []access.ACLEntry `json:"entries"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Entries) != len(actions) {
		t.Fatalf("put ACL response=%s err=%v", body, err)
	}
	return result.Entries
}

func listWorkbenchACL(t *testing.T, server *httptest.Server, auth, tenant, key string) []access.ACLEntry {
	t.Helper()
	response, body := req(t, http.MethodGet, server.URL+"/v1/access/acl?kind=object&bucket=default&key="+url.QueryEscape(key), nil,
		map[string]string{"Authorization": auth, "X-Aero-Tenant": tenant})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list ACL status=%d body=%s", response.StatusCode, body)
	}
	var result struct {
		Entries []access.ACLEntry `json:"entries"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result.Entries
}

func TestObjectACLWorkbenchReadDoesNotGrantManagement(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, operator, "operator-file.txt")
	entries := putWorkbenchACL(t, server, operator, "operator-file.txt", access.ResourceObject, access.EffectAllow, "object:read")
	response, body := req(t, http.MethodGet, server.URL+"/v1/files/operator-file.txt", nil, map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read status=%d body=%s", response.StatusCode, body)
	}
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/v1/access/acl?key=operator-file.txt&kind=object"},
		{http.MethodPut, "/v1/access/acl"}, {http.MethodDelete, "/v1/access/acl/" + entries[0].ID},
	} {
		response, body = req(t, test.method, server.URL+test.path,
			workbenchACLBody("operator-file.txt", access.ResourceObject, access.EffectAllow, "object:write"),
			map[string]string{"Authorization": alice, "Content-Type": "application/json"})
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s ACL status=%d body=%s", test.method, response.StatusCode, body)
		}
	}
}

func TestObjectACLWorkbenchExactKeyDenyAndDelete(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	key := " docs/100% report.txt "
	encoded := strings.ReplaceAll(url.PathEscape(key), "%2F", "/")
	response, body := req(t, http.MethodPut, server.URL+"/v1/files/"+encoded, []byte("content"), map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.StatusCode, body)
	}
	share := createWorkbenchShare(t, server, alice, key)
	entries := putWorkbenchACL(t, server, alice, key, access.ResourceObject, access.EffectDeny, "object:read")
	if entries[0].Key != key || entries[0].Inherit {
		t.Fatalf("ACL target changed: %+v", entries[0])
	}
	for _, target := range []string{server.URL + "/v1/files/" + encoded, share.URL} {
		response, body = req(t, http.MethodGet, target, nil, map[string]string{"Authorization": alice})
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("Deny ignored: status=%d body=%s", response.StatusCode, body)
		}
	}
	response, body = req(t, http.MethodDelete, server.URL+"/v1/access/acl/"+entries[0].ID, nil, map[string]string{"Authorization": operator})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete ACL status=%d body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, share.URL, nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "content" {
		t.Fatalf("access not recovered: status=%d body=%s", response.StatusCode, body)
	}
}

func TestObjectACLWorkbenchEmptyDirectListDoesNotIncludeInheritedDeny(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, alice, "parent/file.txt")
	putWorkbenchACL(t, server, operator, "parent/", access.ResourceFolder, access.EffectDeny, "object:read")
	if entries := listWorkbenchACL(t, server, operator, "default", "parent/file.txt"); len(entries) != 0 {
		t.Fatalf("object direct list included inherited rules: %+v", entries)
	}
	response, body := req(t, http.MethodGet, server.URL+"/v1/files/parent/file.txt", nil, map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("empty direct list bypassed parent Deny: status=%d body=%s", response.StatusCode, body)
	}
}

func TestObjectACLWorkbenchMultiActionFailureIsNotATransaction(t *testing.T) {
	server, _, operator, _ := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, operator, "partial.txt")
	response, body := req(t, http.MethodPut, server.URL+"/v1/access/acl",
		workbenchACLBody("partial.txt", access.ResourceObject, access.EffectAllow, "object:read", "unsupported:action"),
		map[string]string{"Authorization": operator, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid action status=%d body=%s", response.StatusCode, body)
	}
	entries := listWorkbenchACL(t, server, operator, "default", "partial.txt")
	if len(entries) != 1 || entries[0].Action != access.ActionRead {
		t.Fatalf("partial-write boundary changed: %+v", entries)
	}
}

func TestObjectACLWorkbenchTenantIsolationAndSuspension(t *testing.T) {
	server, alice, operator, repo := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, alice, "tenant-acl.txt")
	entries := putWorkbenchACL(t, server, operator, "tenant-acl.txt", access.ResourceObject, access.EffectAllow, "object:read")
	if other := listWorkbenchACL(t, server, operator, "other", "tenant-acl.txt"); len(other) != 0 {
		t.Fatalf("other tenant leaked ACL: %+v", other)
	}
	response, body := req(t, http.MethodDelete, server.URL+"/v1/access/acl/"+entries[0].ID, nil,
		map[string]string{"Authorization": operator, "X-Aero-Tenant": "other"})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant delete status=%d body=%s", response.StatusCode, body)
	}
	if len(listWorkbenchACL(t, server, operator, "default", "tenant-acl.txt")) != 1 {
		t.Fatal("cross-tenant delete removed the default tenant rule")
	}
	assertACLWorkbenchDenied(t, server, alice, entries[0].ID, "other")
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{TenantID: "default", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	assertACLWorkbenchDenied(t, server, operator, entries[0].ID, "default")
}

func assertACLWorkbenchDenied(t *testing.T, server *httptest.Server, auth, id, tenant string) {
	t.Helper()
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/v1/access/acl?key=tenant-acl.txt&kind=object"},
		{http.MethodPut, "/v1/access/acl"}, {http.MethodDelete, "/v1/access/acl/" + id},
	} {
		response, body := req(t, test.method, server.URL+test.path,
			workbenchACLBody("tenant-acl.txt", access.ResourceObject, access.EffectAllow, "object:read"),
			map[string]string{"Authorization": auth, "X-Aero-Tenant": tenant, "Content-Type": "application/json"})
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s tenant=%s ACL status=%d body=%s", test.method, tenant, response.StatusCode, body)
		}
	}
}
