package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func uploadListingFile(t *testing.T, server *httptest.Server, authorization, key string) {
	t.Helper()
	response, body := req(t, http.MethodPut, server.URL+"/v1/files/"+key, []byte("data"),
		map[string]string{"Authorization": authorization})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload %q: status=%d body=%s", key, response.StatusCode, body)
	}
}

func TestOwnedFileViewHTTPUsesAuthenticatedOwnerAndCursor(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	for _, key := range []string{"a-other.txt", "c-other.txt"} {
		uploadListingFile(t, server, operator, key)
	}
	for _, key := range []string{"b-mine.txt", "d-mine.txt"} {
		uploadListingFile(t, server, alice, key)
	}
	for _, test := range []struct {
		query string
		key   string
		more  bool
	}{
		{"view=mine&limit=1&owner=operator&subject_id=operator", "b-mine.txt", true},
		{"view=mine&limit=1&marker=b-mine.txt", "d-mine.txt", false},
	} {
		response, body := req(t, http.MethodGet, server.URL+"/v1/files?"+test.query, nil,
			map[string]string{"Authorization": alice})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("list: status=%d body=%s", response.StatusCode, body)
		}
		var page listResponse
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Objects) != 1 || page.Objects[0].Key != test.key || page.HasMore != test.more {
			t.Fatalf("page=%+v, want key=%s more=%v", page, test.key, test.more)
		}
		if test.more && page.NextMarker != test.key {
			t.Fatalf("foreign cursor=%q", page.NextMarker)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("personalized listing is cacheable")
		}
	}
	response, body := req(t, http.MethodGet, server.URL+"/v1/files?view=all", nil,
		map[string]string{"Authorization": alice})
	var page listResponse
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &page) != nil || len(page.Objects) != 4 {
		t.Fatalf("all view status=%d body=%s", response.StatusCode, body)
	}
}

func TestOwnedFolderViewAndRecycleBinHTTP(t *testing.T) {
	server, alice, operator, repo := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, alice, "mine/owned.txt")
	uploadListingFile(t, server, operator, "other/foreign.txt")
	response, body := req(t, http.MethodGet, server.URL+"/v1/folders?view=mine", nil,
		map[string]string{"Authorization": alice})
	var folders struct {
		Items []folderItem `json:"items"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &folders) != nil ||
		len(folders.Items) != 1 || folders.Items[0].Name != "mine" {
		t.Fatalf("owned folders status=%d body=%s", response.StatusCode, body)
	}
	for _, key := range []string{"mine/owned.txt", "other/foreign.txt"} {
		if err := repo.SoftDeleteObject(context.Background(), "default", "default", key); err != nil {
			t.Fatal(err)
		}
	}
	response, body = req(t, http.MethodGet, server.URL+"/v1/files?view=mine&deleted=true", nil,
		map[string]string{"Authorization": alice})
	var page listResponse
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &page) != nil ||
		len(page.Objects) != 1 || page.Objects[0].Key != "mine/owned.txt" {
		t.Fatalf("owned recycle bin status=%d body=%s", response.StatusCode, body)
	}
}

func TestFileListingViewsRejectAnonymousAndInvalidView(t *testing.T) {
	svc, _, _ := setupTest(t)
	handler := NewHandler(svc, nil)
	for _, endpoint := range []struct {
		path string
		call http.HandlerFunc
	}{{"/v1/files", handler.List}, {"/v1/folders", handler.ListFolders}} {
		for _, test := range []struct {
			view string
			want int
		}{{"mine", http.StatusForbidden}, {"someone-else", http.StatusBadRequest}, {"all", http.StatusOK}} {
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Vary", "Origin")
			request := httptest.NewRequest(http.MethodGet, endpoint.path+"?view="+test.view, nil)
			endpoint.call(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("%s?view=%s: status=%d body=%s", endpoint.path, test.view, recorder.Code, recorder.Body)
			}
			if !strings.Contains(strings.Join(recorder.Header().Values("Vary"), ","), "Origin") {
				t.Fatal("listing overwrote CORS Vary")
			}
		}
	}
}

func TestOwnedListingCannotBypassBucketPolicy(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	uploadListingFile(t, server, alice, "mine/owned.txt")
	policy := `{"Statement":[{"Effect":"Deny","Action":"s3:ListBucket","Resource":"arn:aws:s3:::default"}]}`
	response, body := req(t, http.MethodPut, server.URL+"/v1/buckets/default/policy", bodyPolicy(policy),
		map[string]string{"Authorization": operator, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("set policy status=%d body=%s", response.StatusCode, body)
	}
	for _, path := range []string{"/v1/files?view=mine", "/v1/folders?view=mine"} {
		response, body = req(t, http.MethodGet, server.URL+path, nil, map[string]string{"Authorization": alice})
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("policy bypass %s: status=%d body=%s", path, response.StatusCode, body)
		}
	}
}

func TestFileListingViewIsDocumentedInOpenAPI(t *testing.T) {
	var spec struct {
		Paths map[string]struct {
			Get struct {
				Parameters []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"parameters"`
			} `json:"get"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/files", "/v1/folders"} {
		found := false
		for _, parameter := range spec.Paths[path].Get.Parameters {
			if parameter.Name == "view" && strings.Contains(parameter.Description, "mine") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s lacks view contract", path)
		}
	}
}
