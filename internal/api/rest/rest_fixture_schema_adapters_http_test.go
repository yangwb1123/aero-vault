package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func assertRESTFixtureSchemaAdapterAuthObject(t *testing.T, srv *httptest.Server, token, want string) {
	t.Helper()
	url := srv.URL + "/v1/files/same.txt"
	response, body := req(t, http.MethodPut, url, []byte("unauthorized"), nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("credential-free PUT: status=%d body=%q want 401", response.StatusCode, body)
	}
	response, body = req(t, http.MethodPut, url, []byte(want), map[string]string{"Authorization": token})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("authenticated PUT: status=%d body=%q want 201", response.StatusCode, body)
	}
	assertRESTFixtureSchemaAdapterAuthRead(t, srv, token, want)
}

func assertRESTFixtureSchemaAdapterAuthRead(t *testing.T, srv *httptest.Server, token, want string) {
	t.Helper()
	url := srv.URL + "/v1/files/same.txt"
	response, body := req(t, http.MethodGet, url, nil, map[string]string{"Authorization": token})
	if response.StatusCode != http.StatusOK || string(body) != want {
		t.Fatalf("authenticated GET crossed fixtures: status=%d body=%q want=%q", response.StatusCode, body, want)
	}
	response, body = req(t, http.MethodGet, url, nil, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("credential-free private GET: status=%d body=%q want 403", response.StatusCode, body)
	}
}

func TestRESTFixtureSchemaAdaptersAuthenticatedHTTPIsolation(t *testing.T) {
	cases := []struct {
		name  string
		build func(*testing.T) (*httptest.Server, string)
	}{
		{"newAuthRESTTest", newAuthRESTTest},
		{"newAuthRESTTestWithRepo", func(t *testing.T) (*httptest.Server, string) {
			srv, token, _ := newAuthRESTTestWithRepo(t)
			return srv, token
		}},
		{"newThumbnailAccessHarness", func(t *testing.T) (*httptest.Server, string) {
			srv, token, _ := newThumbnailAccessHarness(t)
			return srv, token
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, tokenA := tc.build(t)
			b, tokenB := tc.build(t)
			assertRESTFixtureSchemaAdapterAuthObject(t, a, tokenA, "private adapter A")
			assertRESTFixtureSchemaAdapterAuthObject(t, b, tokenB, "private adapter B")
			assertRESTFixtureSchemaAdapterPublicRead(t, a, tokenA)
			assertRESTFixtureSchemaAdapterAuthRead(t, b, tokenB, "private adapter B")
			a.Close()
			assertRESTFixtureSchemaAdapterAuthRead(t, b, tokenB, "private adapter B")
			c, tokenC := tc.build(t)
			response, body := req(t, http.MethodGet, c.URL+"/v1/files/same.txt", nil, map[string]string{"Authorization": tokenC})
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("later authenticated fixture inherited data: status=%d body=%q", response.StatusCode, body)
			}
		})
	}
}

func assertRESTFixtureSchemaAdapterPublicRead(t *testing.T, srv *httptest.Server, token string) {
	t.Helper()
	url := srv.URL + "/v1/files/same.txt"
	response, body := req(t, http.MethodPut, url+"/acl", []byte(`{"acl":"public-read"}`), map[string]string{"Authorization": token})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("set public-read ACL: status=%d body=%q", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, url, nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "private adapter A" {
		t.Fatalf("public-read admission changed: status=%d body=%q", response.StatusCode, body)
	}
}
