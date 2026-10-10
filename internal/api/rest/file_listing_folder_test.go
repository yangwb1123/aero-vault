package rest

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestOwnedEmptyFolderHTTP(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	for _, test := range []struct {
		owner string
		path  string
	}{{alice, "mine-empty"}, {operator, "other-empty"}} {
		response, body := req(t, http.MethodPost, server.URL+"/v1/folders/"+test.path, nil,
			map[string]string{"Authorization": test.owner})
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: status=%d body=%s", test.path, response.StatusCode, body)
		}
	}
	response, body := req(t, http.MethodGet, server.URL+"/v1/folders?view=mine", nil,
		map[string]string{"Authorization": alice})
	var listing struct {
		Items []folderItem `json:"items"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &listing) != nil ||
		len(listing.Items) != 1 || listing.Items[0].Name != "mine-empty" {
		t.Fatalf("empty owned folder status=%d body=%s", response.StatusCode, body)
	}
}
