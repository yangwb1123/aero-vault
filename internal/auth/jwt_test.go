package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

const testJWTSecret = "test-secret"

// signRaw mints an HS256 token from arbitrary claims so tests can exercise
// edge cases (missing tenant, empty scopes) that the typed Sign helper guards
// against. Mirrors the signing path in jwt.go.
func signRaw(t *testing.T, secret string, claims jwtClaims) string {
	t.Helper()
	hb, _ := json.Marshal(jwtHeader{Alg: "HS256", Typ: "JWT"})
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}

func TestJWTVerify_TenantAndScopes(t *testing.T) {
	v := NewJWTVerifier(testJWTSecret)
	now := time.Now().Unix()

	tests := []struct {
		name       string
		claims     jwtClaims
		wantErr    bool
		wantTenant string
	}{
		{
			name:       "valid tenant and scopes",
			claims:     jwtClaims{Sub: "k1", Ten: "acme", Scopes: []string{"read", "write"}, Nbf: now},
			wantTenant: "acme",
		},
		{
			name:    "empty tenant claim is rejected",
			claims:  jwtClaims{Sub: "k1", Ten: "", Scopes: []string{"read"}, Nbf: now},
			wantErr: true,
		},
		{
			name:    "missing tenant claim is rejected (no '*' fallback)",
			claims:  jwtClaims{Sub: "k1", Scopes: []string{"read"}, Nbf: now},
			wantErr: true,
		},
		{
			name:    "empty scopes is rejected",
			claims:  jwtClaims{Sub: "k1", Ten: "acme", Scopes: []string{}, Nbf: now},
			wantErr: true,
		},
		{
			name:    "missing scopes is rejected",
			claims:  jwtClaims{Sub: "k1", Ten: "acme", Nbf: now},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok := signRaw(t, testJWTSecret, tc.claims)
			k, err := v.Verify(tok)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got key %+v", k)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if k.Tenant != tc.wantTenant {
				t.Fatalf("tenant = %q, want %q", k.Tenant, tc.wantTenant)
			}
			if k.Tenant == "*" {
				t.Fatal("tenant must never fall back to the '*' admin wildcard")
			}
			for _, s := range tc.claims.Scopes {
				if !k.Scopes[Scope(s)] {
					t.Fatalf("scope %q missing from parsed key", s)
				}
			}
		})
	}
}

func TestJWTVerify_Issuer(t *testing.T) {
	now := time.Now().Unix()
	base := jwtClaims{Sub: "k1", Ten: "acme", Scopes: []string{"read"}, Nbf: now}

	tests := []struct {
		name           string
		expectedIssuer string
		tokenIssuer    string
		wantErr        bool
	}{
		{
			name:           "issuer match ok",
			expectedIssuer: "https://idp.example",
			tokenIssuer:    "https://idp.example",
		},
		{
			name:           "issuer mismatch rejected",
			expectedIssuer: "https://idp.example",
			tokenIssuer:    "https://evil.example",
			wantErr:        true,
		},
		{
			name:           "unset expected issuer ignores 'iss' claim",
			expectedIssuer: "",
			tokenIssuer:    "https://whatever.example",
		},
		{
			name:           "expected issuer but token has no iss is rejected",
			expectedIssuer: "https://idp.example",
			tokenIssuer:    "",
			wantErr:        true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := NewJWTVerifier(testJWTSecret).WithIssuer(tc.expectedIssuer)
			c := base
			c.Iss = tc.tokenIssuer
			tok := signRaw(t, testJWTSecret, c)
			_, err := v.Verify(tok)
			if tc.wantErr && err == nil {
				t.Fatal("expected issuer error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Sign with an issuer configured must stamp 'iss' so the same verifier accepts
// the token round-trip.
func TestJWTSign_StampsIssuer(t *testing.T) {
	v := NewJWTVerifier(testJWTSecret).WithIssuer("https://idp.example")
	tok, err := v.Sign(struct {
		Sub    string
		Tenant string
		Scopes []string
		TTL    time.Duration
	}{Sub: "k1", Tenant: "acme", Scopes: []string{"read"}, TTL: time.Hour})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	k, err := v.Verify(tok)
	if err != nil {
		t.Fatalf("verify round-trip: %v", err)
	}
	if k.Tenant != "acme" {
		t.Fatalf("tenant = %q, want acme", k.Tenant)
	}
	if !k.Scopes[ScopeRead] {
		t.Fatal("read scope missing after round-trip")
	}
}
