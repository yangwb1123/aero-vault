package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type folderListTestPage struct {
	Prefix     string       `json:"prefix"`
	Items      []folderItem `json:"items"`
	HasMore    bool         `json:"has_more"`
	NextMarker string       `json:"next_marker"`
}

func getFolderTestPage(t *testing.T, endpoint, authorization string, query url.Values) folderListTestPage {
	t.Helper()
	response, body := req(t, http.MethodGet, endpoint+"?"+query.Encode(), nil,
		map[string]string{"Authorization": authorization})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("folder list status=%d body=%s", response.StatusCode, body)
	}
	var page folderListTestPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid folder page or cache policy: body=%s cache=%s", body, response.Header.Get("Cache-Control"))
	}
	return page
}

// List fixtures need only metadata; FileService authorization never opens blobs.
func insertFolderListFixture(t *testing.T, repo repository.Repository, tenant, key string) {
	t.Helper()
	_, err := repo.UpsertObject(context.Background(), repository.Object{
		TenantID: tenant, Bucket: "default", Key: key, Backend: "local",
		StorageKey: tenant + "/default/" + key, ETag: "fixture", Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFolderPaginationOnlyMarkerPageAndRepeatedDirectory(t *testing.T) {
	_, repo, server := setupTest(t)
	for _, key := range []string{"docs/", "docs/alpha/a.txt", "docs/alpha/b.txt", "docs/z-last/"} {
		insertFolderListFixture(t, repo, "default", key)
	}
	query := url.Values{"path": {"docs"}, "limit": {"1"}}
	page := getFolderTestPage(t, server.URL+"/folders", "", query)
	if page.Prefix != "docs/" || len(page.Items) != 0 || !page.HasMore || page.NextMarker != "docs/" {
		t.Fatalf("marker-only page=%+v", page)
	}
	for _, test := range []struct {
		name string
		key  string
		more bool
	}{{"alpha", "docs/alpha/a.txt", true}, {"alpha", "docs/alpha/b.txt", true}, {"z-last", "", false}} {
		query.Set("marker", page.NextMarker)
		page = getFolderTestPage(t, server.URL+"/folders", "", query)
		if len(page.Items) != 1 || page.Items[0].Name != test.name || page.HasMore != test.more || page.NextMarker != test.key {
			t.Fatalf("page=%+v, want %+v", page, test)
		}
	}
}

func TestFolderPaginationFindsDirectoriesAfterThousandVisibleObjects(t *testing.T) {
	_, repo, server := setupTest(t)
	for i := range 1001 {
		insertFolderListFixture(t, repo, "default", fmt.Sprintf("a/%04d.txt", i))
	}
	insertFolderListFixture(t, repo, "default", "z-final/file.txt")
	insertFolderListFixture(t, repo, "other", "tenant-private/file.txt")
	for _, limit := range []string{"", "bad", "0", "-1", "1001"} {
		first := getFolderTestPage(t, server.URL+"/folders", "", url.Values{"limit": {limit}})
		if len(first.Items) != 1 || first.Items[0].Name != "a" || !first.HasMore || first.NextMarker != "a/0999.txt" {
			t.Fatalf("limit=%q page=%+v", limit, first)
		}
		next := getFolderTestPage(t, server.URL+"/folders", "", url.Values{"marker": {first.NextMarker}})
		if len(next.Items) != 2 || next.Items[0].Name != "a" || next.Items[1].Name != "z-final" || next.HasMore || next.NextMarker != "" {
			t.Fatalf("late directory lost or foreign tenant exposed: %+v", next)
		}
	}
}

func TestFolderPaginationEmptyListingIsArrayAndTerminal(t *testing.T) {
	_, _, server := setupTest(t)
	page := getFolderTestPage(t, server.URL+"/folders", "", nil)
	if len(page.Items) != 0 || page.HasMore || page.NextMarker != "" {
		t.Fatalf("empty page=%+v", page)
	}
}

func TestFolderPaginationOpenAPIParametersAndContinuation(t *testing.T) {
	var spec struct {
		Paths map[string]struct {
			Get struct {
				Parameters []struct {
					Name string `json:"name"`
				} `json:"parameters"`
			} `json:"get"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, parameter := range spec.Paths["/v1/folders"].Get.Parameters {
		found[parameter.Name] = true
	}
	for _, parameter := range []string{"path", "view", "marker", "limit"} {
		if !found[parameter] {
			t.Fatalf("folder OpenAPI missing %s", parameter)
		}
	}
}
