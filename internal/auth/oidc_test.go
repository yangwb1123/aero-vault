package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOIDCAuthorizationCodePKCEFlow(t *testing.T) {
	var exchanged url.Values
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		exchanged = r.Form
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "signed.access.token", "id_token": "signed.id.token",
			"token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer tokenServer.Close()

	handler, err := NewOIDCHandler(OIDCConfig{
		Issuer: "https://sso.example", ClientID: "aero-vault",
		RedirectURI:           "https://vault.example/auth/oidc/callback",
		AuthorizationEndpoint: "https://sso.example/login/",
		TokenEndpoint:         tokenServer.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	loginReq := httptest.NewRequest(http.MethodGet, "https://vault.example/auth/oidc/login", nil)
	loginRec := httptest.NewRecorder()
	handler.Login(loginRec, loginReq)
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login status = %d", loginRec.Code)
	}
	location, err := url.Parse(loginRec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := location.Query().Get("state")
	if state == "" || location.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization redirect = %s", location)
	}
	if location.Query().Get("code_challenge") == "" {
		t.Fatal("authorization redirect omitted code_challenge")
	}

	callbackReq := httptest.NewRequest(http.MethodGet,
		"https://vault.example/auth/oidc/callback?code=code-1&state="+url.QueryEscape(state), nil)
	callbackReq.AddCookie(loginRec.Result().Cookies()[0])
	callbackRec := httptest.NewRecorder()
	handler.Callback(callbackRec, callbackReq)
	if callbackRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d body=%s", callbackRec.Code, callbackRec.Body.String())
	}
	redirect := callbackRec.Header().Get("Location")
	if !strings.HasPrefix(redirect, "/ui#") || !strings.Contains(redirect, "oidc_access_token=") {
		t.Fatalf("callback redirect = %q", redirect)
	}
	if exchanged.Get("client_id") != "aero-vault" || exchanged.Get("code_verifier") == "" {
		t.Fatalf("token exchange form = %#v", exchanged)
	}
	if exchanged.Get("redirect_uri") != "https://vault.example/auth/oidc/callback" {
		t.Fatalf("redirect_uri = %q", exchanged.Get("redirect_uri"))
	}
}

func TestOIDCCallbackRejectsStateWithoutBrowserCookie(t *testing.T) {
	handler, err := NewOIDCHandler(OIDCConfig{
		Issuer: "https://sso.example", ClientID: "aero-vault",
		RedirectURI: "https://vault.example/auth/oidc/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"https://vault.example/auth/oidc/callback?code=x&state=observed", nil)
	handler.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestOIDCDefaultsToSnaplinkAuthorizationEndpoint(t *testing.T) {
	handler, err := NewOIDCHandler(OIDCConfig{
		Issuer: "https://sso.example", ClientID: "aero-vault",
		RedirectURI: "https://vault.example/auth/oidc/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.Login(rec, httptest.NewRequest(http.MethodGet, "https://vault.example/auth/oidc/login", nil))
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Path != "/auth/login" {
		t.Fatalf("authorization path = %q, want /auth/login", location.Path)
	}
}
