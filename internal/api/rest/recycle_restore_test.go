package rest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func TestRecycleRestoreHTTPKeepsQuotaAndConflictChecks(t *testing.T) {
	ctx := context.Background()
	svc, repo, server := setupTest(t)
	for _, key := range []string{"a.txt", "b.txt"} {
		if _, err := svc.Put(ctx, "default", "default", key, strings.NewReader("data"), 4, service.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := svc.Delete(ctx, "default", "default", key, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetTenantQuota(ctx, "default", 4, 1); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		key  string
		want int
	}{{"a.txt", http.StatusOK}, {"b.txt", http.StatusInsufficientStorage}, {"a.txt", http.StatusPreconditionFailed}} {
		response, body := req(t, http.MethodPost, server.URL+"/files/"+test.key+"/restore", nil, nil)
		if response.StatusCode != test.want {
			t.Fatalf("restore %s status=%d want=%d body=%s", test.key, response.StatusCode, test.want, body)
		}
	}
	usage, err := repo.GetTenantQuota(ctx, "default")
	if err != nil || usage.UsedBytes != 4 || usage.UsedObjects != 1 {
		t.Fatalf("failed/repeated restore changed usage: usage=%+v err=%v", usage, err)
	}
	response, body := req(t, http.MethodGet, server.URL+"/files/a.txt", nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "data" {
		t.Fatalf("conflict overwrote restored bytes: status=%d body=%s", response.StatusCode, body)
	}
	if _, err := repo.GetObject(ctx, "default", "default", "b.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("over-quota object became active: %v", err)
	}
}

func TestRecycleRestoreHTTPKeepsDenyTenantStatusAndIsolation(t *testing.T) {
	server, alice, operator, repo := newEnterpriseRESTTest(t)
	for _, key := range []string{"denied.txt", "permitted.txt", "suspended.txt"} {
		uploadListingFile(t, server, alice, key)
		if err := repo.SoftDeleteObject(context.Background(), "default", "default", key); err != nil {
			t.Fatal(err)
		}
	}
	response, body := req(t, http.MethodPut, server.URL+"/v1/access/acl", []byte(`{
		"bucket":"default","key":"denied.txt","resource_kind":"object",
		"principal_type":"everyone","actions":["object:restore"],"effect":"deny"
	}`), map[string]string{"Authorization": operator, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("restore deny ACL status=%d body=%s", response.StatusCode, body)
	}
	for _, test := range []struct {
		key  string
		want int
	}{{"denied.txt", http.StatusForbidden}, {"permitted.txt", http.StatusOK}} {
		response, body = req(t, http.MethodPost, server.URL+"/v1/files/"+test.key+"/restore", nil,
			map[string]string{"Authorization": alice})
		if response.StatusCode != test.want {
			t.Fatalf("restore %s status=%d body=%s", test.key, response.StatusCode, body)
		}
	}
	response, body = req(t, http.MethodPost, server.URL+"/v1/files/suspended.txt/restore", nil,
		map[string]string{"Authorization": alice, "X-Aero-Tenant": "other"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant mismatch restore status=%d body=%s", response.StatusCode, body)
	}
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{TenantID: "default", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	response, body = req(t, http.MethodPost, server.URL+"/v1/files/suspended.txt/restore", nil,
		map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled tenant restore status=%d body=%s", response.StatusCode, body)
	}
}

type unavailableRestoreProvider struct{}

func (unavailableRestoreProvider) Authorize(
	_ context.Context, _ access.Principal, action access.Action, _ access.Resource,
) (access.Decision, error) {
	if action == access.ActionRestore {
		return access.Decision{}, errors.New("restore provider unavailable")
	}
	return access.Decision{Allowed: true}, nil
}

func TestRecycleRestoreHTTPFailsClosedOnProviderFailure(t *testing.T) {
	ctx := context.Background()
	svc, repo, server := setupTest(t)
	if _, err := svc.Put(ctx, "default", "default", "blocked.txt", strings.NewReader("data"), 4, service.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "default", "default", "blocked.txt", false); err != nil {
		t.Fatal(err)
	}
	svc.WithAuthorizer(unavailableRestoreProvider{})
	response, body := req(t, http.MethodPost, server.URL+"/files/blocked.txt/restore", nil, nil)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("provider failure restore status=%d body=%s", response.StatusCode, body)
	}
	if _, err := repo.GetObject(ctx, "default", "default", "blocked.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("provider failure restored object: %v", err)
	}
}
