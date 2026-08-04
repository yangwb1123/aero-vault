package config

import "testing"

func TestValidateOIDCRequiresPinnedJWKS(t *testing.T) {
	cfg := baseValid()
	cfg.Auth.OIDCIssuer = "https://sso.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("partial OIDC configuration should fail")
	}

	cfg.Auth.OIDCClientID = "aero-vault"
	cfg.Auth.OIDCRedirectURI = "https://vault.example/auth/oidc/callback"
	cfg.Auth.JWKSEndpoint = "https://sso.example/.well-known/jwks.json"
	cfg.Auth.JWTIssuer = "https://sso.example"
	cfg.Auth.JWKSAudience = "aero-vault"
	cfg.Auth.JWKSTenantClaim = "sub"
	cfg.Auth.JWKSDefaultScopes = []string{"read", "write"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid OIDC config: %v", err)
	}
}

func TestValidateJWKSMapping(t *testing.T) {
	cfg := baseValid()
	cfg.Auth.JWKSTenantClaim = "unsafe"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown tenant claim should fail")
	}
	cfg.Auth.JWKSTenantClaim = "ten"
	cfg.Auth.JWKSDefaultScopes = []string{"owner"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown fallback scope should fail")
	}
}
