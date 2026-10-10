package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestFolderPaginationMineKeepsDenyAndTenantSuspension(t *testing.T) {
	server, alice, operator, repo := newEnterpriseRESTTest(t)
	for _, key := range []string{"a-mine/file.txt", "b-denied/secret.txt", "c-mine/file.txt"} {
		uploadListingFile(t, server, alice, key)
	}
	uploadListingFile(t, server, operator, "b-other/foreign.txt")
	response, body := req(t, http.MethodPut, server.URL+"/v1/access/acl", []byte(`{
		"bucket":"default","key":"b-denied/","resource_kind":"folder",
		"principal_type":"everyone","actions":["object:read"],"effect":"deny","inherit":true
	}`), map[string]string{"Authorization": operator, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("deny ACL status=%d body=%s", response.StatusCode, body)
	}
	endpoint := server.URL + "/v1/folders"
	query := url.Values{"view": {"mine"}, "limit": {"1"}}
	first := getFolderTestPage(t, endpoint, alice, query)
	if len(first.Items) != 1 || first.Items[0].Name != "a-mine" || !first.HasMore || first.NextMarker != "a-mine/file.txt" {
		t.Fatalf("first owned page=%+v", first)
	}
	query.Set("marker", first.NextMarker)
	next := getFolderTestPage(t, endpoint, alice, query)
	if len(next.Items) != 1 || next.Items[0].Name != "c-mine" || next.HasMore || next.NextMarker != "" {
		t.Fatalf("denied/foreign folder affected page=%+v", next)
	}
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{TenantID: "default", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	response, body = req(t, http.MethodGet, endpoint+"?"+query.Encode(), nil, map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("suspended tenant continuation status=%d body=%s", response.StatusCode, body)
	}
}

type folderReadUnavailableProvider struct{}

func (folderReadUnavailableProvider) Authorize(
	_ context.Context, _ access.Principal, action access.Action, _ access.Resource,
) (access.Decision, error) {
	if action == access.ActionRead {
		return access.Decision{}, errors.New("read provider unavailable")
	}
	return access.Decision{Allowed: true}, nil
}

func TestFolderPaginationFailsClosedOnProviderFailure(t *testing.T) {
	svc, repo, _ := setupTest(t)
	insertFolderListFixture(t, repo, "default", "visible/file.txt")
	svc.WithAuthorizer(folderReadUnavailableProvider{})
	request := httptest.NewRequest(http.MethodGet, "/v1/folders?marker=before&limit=1", nil)
	request = request.WithContext(access.WithPrincipal(request.Context(), access.Principal{
		SubjectID: "reader", TenantID: "default", Kind: access.PrincipalUser, Scopes: []string{"read"},
	}))
	recorder := httptest.NewRecorder()
	NewHandler(svc, nil).ListFolders(recorder, request)
	if recorder.Code == http.StatusOK {
		t.Fatalf("provider failure returned folder data: %s", recorder.Body)
	}
}
