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

func TestJWKSVerifierAcceptsPinnedEdDSAToken(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks := map[string]any{"keys": []map[string]any{{
		"kty": "OKP", "use": "sig", "alg": "EdDSA", "kid": "snaplink-1",
		"crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(publicKey),
	}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer server.Close()

	token := signEdDSATestToken(t, privateKey, map[string]any{
		"iss": "https://sso.example", "sub": "user-42", "client_id": "aero-vault",
		"scope": "openid profile", "exp": time.Now().Add(time.Minute).Unix(),
	})
	verifier := NewJWKSVerifier(server.URL, time.Minute, "https://sso.example").
		WithAudience("aero-vault").
		WithTenantClaim("sub").
		WithDefaultScopes([]Scope{ScopeRead, ScopeWrite})
	key, err := verifier.Verify(token)
	if err != nil {
		t.Fatalf("verify EdDSA token: %v", err)
	}
	if key.Tenant != "user-42" || !key.Has(ScopeRead) || !key.Has(ScopeWrite) {
		t.Fatalf("mapped key = tenant %q scopes %#v", key.Tenant, key.Scopes)
	}

	wrongClient := NewJWKSVerifier(server.URL, time.Minute, "https://sso.example").
		WithAudience("other-client").
		WithTenantClaim("sub").
		WithDefaultScopes([]Scope{ScopeRead})
	if _, err := wrongClient.Verify(token); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("wrong client error = %v", err)
	}
}

func TestJWKSVerifierMapsSnaplinkClientToTenant(t *testing.T) {
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
	defer server.Close()
	token := signEdDSATestToken(t, privateKey, map[string]any{
		"iss": "https://sso.example", "sub": "user-42", "client_id": "aero-vault",
		"scope": "read write", "exp": time.Now().Add(time.Minute).Unix(),
	})
	verifier := NewJWKSVerifier(server.URL, time.Minute, "https://sso.example").
		WithAudience("aero-vault").
		WithClientTenants(map[string]string{"aero-vault": "acme"})
	key, err := verifier.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if key.Tenant != "acme" || key.SubjectID != "user-42" {
		t.Fatalf("key tenant=%q subject=%q", key.Tenant, key.SubjectID)
	}
	verifier.WithClientTenants(map[string]string{"another-client": "other"})
	if _, err := verifier.Verify(token); err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("unmapped client must fail closed, err=%v", err)
	}
}

func signEdDSATestToken(t *testing.T, privateKey ed25519.PrivateKey, claims map[string]any) string {
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
	signature := ed25519.Sign(privateKey, []byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}
