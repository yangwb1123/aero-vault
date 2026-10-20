package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func putPreviewVersion(t *testing.T, server *httptest.Server, authorization, key, body string) string {
	t.Helper()
	encoded := strings.ReplaceAll(url.PathEscape(key), "%2F", "/")
	response, result := req(t, http.MethodPut, server.URL+"/v1/files/"+encoded, []byte(body),
		map[string]string{"Authorization": authorization, "Content-Type": "text/plain"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.StatusCode, result)
	}
	response, result = req(t, http.MethodGet, server.URL+"/v1/files/"+encoded+"/versions", nil,
		map[string]string{"Authorization": authorization})
	var listing struct {
		Versions []versionDTO `json:"versions"`
	}
	if err := json.Unmarshal(result, &listing); err != nil || response.StatusCode != http.StatusOK || len(listing.Versions) == 0 {
		t.Fatalf("version list status=%d body=%s err=%v", response.StatusCode, result, err)
	}
	return listing.Versions[0].VersionID
}

func enablePreviewVersioning(t *testing.T, server *httptest.Server, operator string) {
	t.Helper()
	response, body := req(t, http.MethodPut, server.URL+"/v1/buckets/default/versioning", []byte(`{"enabled":true}`),
		map[string]string{"Authorization": operator, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("enable versioning status=%d body=%s", response.StatusCode, body)
	}
}

func previewVersionURL(server *httptest.Server, key, id string) string {
	return server.URL + "/v1/files/" + strings.ReplaceAll(url.PathEscape(key), "%2F", "/") + "?version=" + url.QueryEscape(id)
}

func TestObjectVersionPreviewRangeStaysPinned(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	enablePreviewVersioning(t, server, operator)
	key, old := " docs/100% report.txt ", strings.Repeat("old-data", 1024)
	id := putPreviewVersion(t, server, alice, key, old)
	putPreviewVersion(t, server, alice, key, strings.Repeat("new-data", 1024))
	for _, test := range []struct{ header, content, contentRange string }{
		{"bytes=0-4095", old[:4096], "bytes 0-4095/8192"},
		{"bytes=4096-", old[4096:], "bytes 4096-8191/8192"},
		{"bytes=-8", old[len(old)-8:], "bytes 8184-8191/8192"},
	} {
		response, body := req(t, http.MethodGet, previewVersionURL(server, key, id), nil,
			map[string]string{"Authorization": alice, "Range": test.header})
		if response.StatusCode != http.StatusPartialContent || string(body) != test.content {
			t.Fatalf("range=%s status=%d bytes=%d want pinned %d bytes", test.header, response.StatusCode, len(body), len(test.content))
		}
		if response.Header.Get("Content-Range") != test.contentRange || response.Header.Get("X-Version-Id") != id {
			t.Fatalf("range response headers=%v", response.Header)
		}
		if response.ContentLength != int64(len(test.content)) || response.Header.Get("Accept-Ranges") != "bytes" {
			t.Fatalf("range length/accept headers=%v", response.Header)
		}
	}
}

func TestObjectVersionPreviewPinWinsOverSubresourceSuffix(t *testing.T) {
	svc, _, server := setupTest(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"tags", "versions", "acl", "metadata"} {
		key := "literal/" + suffix
		object, err := svc.Put(ctx, "", "", key, strings.NewReader("old bytes"), 9, service.PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Put(ctx, "", "", key, strings.NewReader("new bytes"), 9, service.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		response, body := req(t, http.MethodGet, server.URL+"/files/"+key+"?version="+object.VersionID, nil,
			map[string]string{"Range": "bytes=0-2"})
		if response.StatusCode != http.StatusPartialContent || string(body) != "old" || response.Header.Get("X-Version-Id") != object.VersionID {
			t.Fatalf("literal suffix=%s status=%d body=%s", suffix, response.StatusCode, body)
		}
	}
}

func TestObjectVersionPreviewRangeConditions(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	enablePreviewVersioning(t, server, operator)
	id := putPreviewVersion(t, server, alice, "conditions.txt", "old content")
	endpoint := previewVersionURL(server, "conditions.txt", id)
	response, _ := req(t, http.MethodGet, endpoint, nil, map[string]string{"Authorization": alice})
	etag, modified := response.Header.Get("ETag"), response.Header.Get("Last-Modified")
	for _, test := range []struct {
		header, value string
		status        int
		content       string
	}{
		{"If-Range", etag, 206, "old"}, {"If-Range", modified, 206, "old"},
		{"If-Range", `"different"`, 200, "old content"}, {"If-Range", "W/" + etag, 200, "old content"},
		{"If-Range", "*", 200, "old content"}, {"If-Range", "Wed, 01 Jan 2020 00:00:00 GMT", 200, "old content"},
		{"If-Match", `"different"`, 412, ""}, {"If-None-Match", etag, 304, ""},
	} {
		headers := map[string]string{"Authorization": alice, "Range": "bytes=0-2", test.header: test.value}
		response, body := req(t, http.MethodGet, endpoint, nil, headers)
		if response.StatusCode != test.status || (test.status < 400 && string(body) != test.content) {
			t.Fatalf("%s=%s status=%d body=%q", test.header, test.value, response.StatusCode, body)
		}
		if test.status < 400 && response.Header.Get("X-Version-Id") != id {
			t.Fatalf("conditional response lost pin: %v", response.Header)
		}
	}
}

func TestObjectVersionPreviewBadRangeAndIDDoNotFallBack(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	enablePreviewVersioning(t, server, operator)
	id := putPreviewVersion(t, server, alice, "short.txt", "old")
	putPreviewVersion(t, server, alice, "short.txt", "latest")
	response, body := req(t, http.MethodGet, previewVersionURL(server, "short.txt", id), nil,
		map[string]string{"Authorization": alice, "Range": "bytes=99-100"})
	if response.StatusCode != http.StatusRequestedRangeNotSatisfiable || response.Header.Get("Content-Range") != "bytes */3" {
		t.Fatalf("bad range status=%d headers=%v body=%s", response.StatusCode, response.Header, body)
	}
	response, body = req(t, http.MethodGet, previewVersionURL(server, "short.txt", "missing"), nil,
		map[string]string{"Authorization": alice, "Range": "bytes=0-2"})
	if response.StatusCode != http.StatusNotFound || strings.Contains(string(body), "latest") {
		t.Fatalf("missing pin status=%d body=%s", response.StatusCode, body)
	}
}

func TestObjectVersionPreviewKeepsDenyAndTenantBoundaries(t *testing.T) {
	server, alice, operator, repo := newEnterpriseRESTTest(t)
	enablePreviewVersioning(t, server, operator)
	id := putPreviewVersion(t, server, alice, "private.txt", "old confidential")
	endpoint := previewVersionURL(server, "private.txt", id)
	putWorkbenchACL(t, server, alice, "private.txt", access.ResourceObject, access.EffectDeny, "object:read")
	response, body := req(t, http.MethodGet, endpoint, nil, map[string]string{"Authorization": alice, "Range": "bytes=0-3"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("Deny ignored: status=%d body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, endpoint, nil, map[string]string{"Authorization": alice, "X-Aero-Tenant": "other"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant-bound credential escaped: status=%d body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, endpoint, nil, map[string]string{"Authorization": operator, "X-Aero-Tenant": "other"})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("operator read another tenant version: status=%d body=%s", response.StatusCode, body)
	}
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{TenantID: "default", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	response, body = req(t, http.MethodGet, endpoint, nil, map[string]string{"Authorization": operator, "Range": "bytes=0-3"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("suspended historical read status=%d body=%s", response.StatusCode, body)
	}
}

func TestObjectVersionPreviewProviderFailureDoesNotReturnBytes(t *testing.T) {
	svc, _, server := setupTest(t)
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	object, err := svc.Put(context.Background(), "", "", "provider.txt", strings.NewReader("secret"), 6, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	svc.WithAuthorizer(folderReadUnavailableProvider{})
	response, body := req(t, http.MethodGet, server.URL+"/files/provider.txt?version="+object.VersionID, nil,
		map[string]string{"Range": "bytes=0-2"})
	if response.StatusCode < 400 || strings.Contains(string(body), "secret") {
		t.Fatalf("failed provider returned bytes: status=%d body=%s", response.StatusCode, body)
	}
}

func TestObjectVersionPreviewSoftDeletedVersionIsNotDeleteMarker(t *testing.T) {
	svc, _, server := setupTest(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	object, err := svc.Put(ctx, "", "", "deleted.txt", strings.NewReader("content"), 7, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "", "", "deleted.txt", false); err != nil {
		t.Fatal(err)
	}
	marker, err := svc.CreateDeleteMarker(ctx, "", "", "deleted.txt")
	if err != nil {
		t.Fatal(err)
	}
	response, body := req(t, http.MethodGet, server.URL+"/files/deleted.txt/versions", nil, nil)
	var listing struct {
		Versions []versionDTO `json:"versions"`
	}
	if err := json.Unmarshal(body, &listing); err != nil || response.StatusCode != 200 || len(listing.Versions) != 2 {
		t.Fatalf("versions status=%d body=%s err=%v", response.StatusCode, body, err)
	}
	for _, version := range listing.Versions {
		if version.IsDeleteMarker != (version.VersionID == marker.VersionID) {
			t.Fatalf("marker confused with soft deletion: %+v", version)
		}
	}
	response, body = req(t, http.MethodGet, server.URL+"/files/deleted.txt?version="+object.VersionID, nil, map[string]string{"Range": "bytes=0-2"})
	if response.StatusCode != http.StatusPartialContent || string(body) != "con" {
		t.Fatalf("soft-deleted version unreadable: status=%d body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, server.URL+"/files/deleted.txt?version="+marker.VersionID, nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("marker returned bytes: status=%d body=%s", response.StatusCode, body)
	}
}

func TestObjectVersionPreviewOpenAPIVersionParameter(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	operation := spec["paths"].(map[string]any)["/v1/files/{key}"].(map[string]any)["get"].(map[string]any)
	found := false
	for _, value := range operation["parameters"].([]any) {
		parameter := value.(map[string]any)
		found = found || parameter["name"] == "version" && parameter["in"] == "query"
	}
	if !found {
		t.Fatal("historical version query parameter missing from OpenAPI")
	}
	for _, status := range []int{206, 304, 412, 416} {
		if operation["responses"].(map[string]any)[fmt.Sprint(status)] == nil {
			t.Fatalf("historical range/conditional response %d missing", status)
		}
	}
}
