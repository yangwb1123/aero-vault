package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSnaplinkVerifierMapsVerifiedIdentity(t *testing.T) {
	server, privateKey := snaplinkTestIssuer(t)
	token := signSnaplinkTestToken(t, privateKey, map[string]any{
		"iss": "https://sso.example", "sub": "user-42", "client_id": "aero-vault",
		"scope": "openid profile", "roles": []string{"editor"}, "groups": []string{"engineering"},
		"exp": time.Now().Add(time.Minute).Unix(),
	})
	verifier, err := NewSnaplinkVerifier(t.Context(), SnaplinkConfig{
		Issuer: "https://sso.example", JWKSURL: server.URL, Audience: "aero-vault",
		TenantClaim: "sub", DefaultScopes: []Scope{ScopeRead, ScopeWrite},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.Close()
	key, err := verifier.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("verify Snaplink token: %v", err)
	}
	if key.Tenant != "user-42" || key.SubjectID != "user-42" {
		t.Fatalf("mapped key tenant=%q subject=%q", key.Tenant, key.SubjectID)
	}
	if !key.Has(ScopeRead) || !key.Has(ScopeWrite) {
		t.Fatalf("mapped scopes = %#v", key.Scopes)
	}
	if len(key.Roles) != 1 || key.Roles[0] != "editor" || len(key.Groups) != 1 {
		t.Fatalf("mapped roles/groups = %#v/%#v", key.Roles, key.Groups)
	}
}

func TestSnaplinkVerifierPinsClientAndTenant(t *testing.T) {
	server, privateKey := snaplinkTestIssuer(t)
	token := signSnaplinkTestToken(t, privateKey, map[string]any{
		"iss": "https://sso.example", "sub": "user-42", "client_id": "aero-vault",
		"scope": "read write", "exp": time.Now().Add(time.Minute).Unix(),
	})
	verifier, err := NewSnaplinkVerifier(t.Context(), SnaplinkConfig{
		Issuer: "https://sso.example", JWKSURL: server.URL, Audience: "aero-vault",
		ClientTenants: map[string]string{"aero-vault": "acme"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.Close()
	key, err := verifier.Verify(t.Context(), token)
	if err != nil || key.Tenant != "acme" {
		t.Fatalf("mapped key=%#v err=%v", key, err)
	}

	wrong, err := NewSnaplinkVerifier(t.Context(), SnaplinkConfig{
		Issuer: "https://sso.example", JWKSURL: server.URL, Audience: "other-client",
		ClientTenants: map[string]string{"aero-vault": "acme"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if _, err := wrong.Verify(t.Context(), token); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("wrong client error = %v", err)
	}
}

func TestSnaplinkVerifierRejectsUnmappedScopes(t *testing.T) {
	server, privateKey := snaplinkTestIssuer(t)
	token := signSnaplinkTestToken(t, privateKey, map[string]any{
		"iss": "https://sso.example", "sub": "user-42", "client_id": "aero-vault",
		"scope": "openid profile", "exp": time.Now().Add(time.Minute).Unix(),
	})
	verifier, err := NewSnaplinkVerifier(t.Context(), SnaplinkConfig{
		Issuer: "https://sso.example", JWKSURL: server.URL,
		Audience: "aero-vault", TenantClaim: "sub",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.Close()
	if _, err := verifier.Verify(t.Context(), token); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("unmapped scope error = %v", err)
	}
}

func snaplinkTestIssuer(t *testing.T) (*httptest.Server, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "OKP", "use": "sig", "alg": "EdDSA", "kid": "snaplink-1",
			"crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(publicKey),
		}}})
	}))
	t.Cleanup(server.Close)
	return server, privateKey
}

func signSnaplinkTestToken(t *testing.T, privateKey ed25519.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "snaplink-1", "typ": "at+jwt"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	input := encodedHeader + "." + encodedPayload
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(input)))
}
