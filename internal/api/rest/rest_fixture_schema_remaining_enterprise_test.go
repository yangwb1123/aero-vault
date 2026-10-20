package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestRESTFixtureSchemaRemainingEnterpriseIsolation(t *testing.T) {
	a, tokenA, _, repoA := newEnterpriseRESTTest(t)
	b, tokenB, _, repoB := newEnterpriseRESTTest(t)
	if repoA == repoB {
		t.Fatal("enterprise factories shared a writable repository")
	}
	assertRESTFixtureRemainingEnterpriseObject(t, a, tokenA, "enterprise A")
	assertRESTFixtureRemainingEnterpriseObject(t, b, tokenB, "enterprise B")
	if err := repoA.UpsertTenant(context.Background(), repository.TenantRecord{
		TenantID: "default", DisplayName: "Default", Status: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
	response, body := req(t, http.MethodGet, a.URL+"/v1/files/same.txt", nil,
		map[string]string{"Authorization": tokenA})
	if response.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "TenantDisabled") {
		t.Fatalf("disabled A tenant: status=%d body=%q", response.StatusCode, body)
	}
	assertRESTFixtureRemainingEnterpriseRead(t, b, tokenB, "enterprise B")
	a.Close()
	if err := repoA.Close(); err != nil {
		t.Fatal(err)
	}
	assertRESTFixtureRemainingEnterpriseRead(t, b, tokenB, "enterprise B")
	c, tokenC, _, repoC := newEnterpriseRESTTest(t)
	response, body = req(t, http.MethodGet, c.URL+"/v1/files/same.txt", nil,
		map[string]string{"Authorization": tokenC})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("later enterprise factory inherited object/status: status=%d body=%q", response.StatusCode, body)
	}
	if _, err := repoC.GetObject(context.Background(), "default", "default", "same.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("later enterprise repository inherited object: %v", err)
	}
}

func assertRESTFixtureRemainingEnterpriseObject(t *testing.T, srv *httptest.Server, token, want string) {
	t.Helper()
	url := srv.URL + "/v1/files/same.txt"
	response, body := req(t, http.MethodPut, url, []byte("unauthorized"), nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("credential-free PUT: status=%d body=%q", response.StatusCode, body)
	}
	response, body = req(t, http.MethodPut, url, []byte(want), map[string]string{"Authorization": token})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("authenticated PUT: status=%d body=%q", response.StatusCode, body)
	}
	assertRESTFixtureRemainingEnterpriseRead(t, srv, token, want)
}

func assertRESTFixtureRemainingEnterpriseRead(t *testing.T, srv *httptest.Server, token, want string) {
	t.Helper()
	for _, tc := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"authenticated", map[string]string{"Authorization": token}, http.StatusOK},
		{"credential-free", nil, http.StatusUnauthorized},
		{"tenant conflict", map[string]string{"Authorization": token, "X-Aero-Tenant": "other"}, http.StatusForbidden},
	} {
		response, body := req(t, http.MethodGet, srv.URL+"/v1/files/same.txt", nil, tc.headers)
		if response.StatusCode != tc.status {
			t.Fatalf("%s GET: status=%d body=%q want=%d", tc.name, response.StatusCode, body, tc.status)
		}
		if tc.status == http.StatusOK && string(body) != want {
			t.Fatalf("GET crossed factory contents: body=%q want=%q", body, want)
		}
	}
}
