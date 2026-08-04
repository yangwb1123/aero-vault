package auth

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// JWKSVerifier validates RS256 and EdDSA JWTs against a rotating JWKS.
type JWKSVerifier struct {
	provider      *JWKSProvider
	issuer        string
	audience      string
	tenantClaim   string
	clientTenants map[string]string
	defaultScopes []Scope
}

type externalJWTHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid,omitempty"`
}

type externalJWTClaims struct {
	Sub      string          `json:"sub,omitempty"`
	Ten      string          `json:"ten,omitempty"`
	TenantID string          `json:"tenant_id,omitempty"`
	Iss      string          `json:"iss,omitempty"`
	Aud      json.RawMessage `json:"aud,omitempty"`
	ClientID string          `json:"client_id,omitempty"`
	Azp      string          `json:"azp,omitempty"`
	Scope    string          `json:"scope,omitempty"`
	Scopes   []string        `json:"scopes,omitempty"`
	Roles    []string        `json:"roles,omitempty"`
	Groups   []string        `json:"groups,omitempty"`
	Exp      int64           `json:"exp,omitempty"`
	Nbf      int64           `json:"nbf,omitempty"`
}

func NewJWKSVerifier(jwksURL string, ttl time.Duration, issuer string) *JWKSVerifier {
	return &JWKSVerifier{
		provider:    NewJWKSProvider(jwksURL, ttl),
		issuer:      issuer,
		tenantClaim: "ten",
	}
}

// NewRS256Verifier remains as a compatibility constructor; verification now
// also accepts EdDSA when the JWK and JWT header both declare it.
func NewRS256Verifier(jwksURL string, ttl time.Duration, issuer string) *JWKSVerifier {
	return NewJWKSVerifier(jwksURL, ttl, issuer)
}

func (v *JWKSVerifier) WithAudience(audience string) *JWKSVerifier {
	v.audience = audience
	return v
}

func (v *JWKSVerifier) WithTenantClaim(claim string) *JWKSVerifier {
	if claim != "" {
		v.tenantClaim = claim
	}
	return v
}

func (v *JWKSVerifier) WithDefaultScopes(scopes []Scope) *JWKSVerifier {
	v.defaultScopes = append([]Scope(nil), scopes...)
	return v
}

// WithClientTenants maps a trusted OAuth client_id/azp to its Aero tenant.
// Snaplink deliberately omits tenant_id from access tokens; its tenant-bound
// client is therefore the stable, issuer-validated tenancy signal.
func (v *JWKSVerifier) WithClientTenants(clientTenants map[string]string) *JWKSVerifier {
	v.clientTenants = make(map[string]string, len(clientTenants))
	for clientID, tenant := range clientTenants {
		if clientID != "" && tenant != "" {
			v.clientTenants[clientID] = tenant
		}
	}
	return v
}

func (v *JWKSVerifier) Verify(token string) (Key, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Key{}, errors.New("jwt: not a 3-part token")
	}
	header, err := parseExternalJWTHeader(parts[0])
	if err != nil {
		return Key{}, err
	}
	publicKey, err := v.provider.GetKey(header.Kid)
	if err != nil {
		return Key{}, fmt.Errorf("jwt: get key: %w", err)
	}
	if err := verifyExternalSignature(header, publicKey, parts); err != nil {
		return Key{}, err
	}
	claims, err := parseExternalClaims(parts[1])
	if err != nil {
		return Key{}, err
	}
	return v.claimsToKey(token, claims)
}

func parseExternalJWTHeader(part string) (externalJWTHeader, error) {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return externalJWTHeader{}, fmt.Errorf("jwt: decode header: %w", err)
	}
	var header externalJWTHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return header, fmt.Errorf("jwt: parse header: %w", err)
	}
	if header.Alg != "RS256" && header.Alg != "EdDSA" {
		return header, fmt.Errorf("jwt: unsupported alg %q", header.Alg)
	}
	if header.Kid == "" {
		return header, errors.New("jwt: token must have 'kid' header")
	}
	return header, nil
}

func verifyExternalSignature(header externalJWTHeader, publicKey jwksPublicKey, parts []string) error {
	if publicKey.alg != "" && publicKey.alg != header.Alg {
		return errors.New("jwt: key algorithm mismatch")
	}
	signingInput := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("jwt: decode sig: %w", err)
	}
	switch key := publicKey.key.(type) {
	case *rsa.PublicKey:
		hash := sha256.Sum256([]byte(signingInput))
		if header.Alg != "RS256" || rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature) != nil {
			return errors.New("jwt: bad signature")
		}
	case ed25519.PublicKey:
		if header.Alg != "EdDSA" || !ed25519.Verify(key, []byte(signingInput), signature) {
			return errors.New("jwt: bad signature")
		}
	default:
		return errors.New("jwt: unsupported public key")
	}
	return nil
}

func parseExternalClaims(part string) (externalJWTClaims, error) {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return externalJWTClaims{}, fmt.Errorf("jwt: decode claims: %w", err)
	}
	var claims externalJWTClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return claims, fmt.Errorf("jwt: parse claims: %w", err)
	}
	return claims, nil
}

func (v *JWKSVerifier) claimsToKey(token string, claims externalJWTClaims) (Key, error) {
	if err := v.validateClaims(claims); err != nil {
		return Key{}, err
	}
	tenant := claims.tenant(v.tenantClaim)
	if len(v.clientTenants) > 0 {
		clientID := claims.ClientID
		if clientID == "" {
			clientID = claims.Azp
		}
		tenant = v.clientTenants[clientID]
	}
	if tenant == "" {
		return Key{}, errors.New("jwt: missing tenant claim")
	}
	scopes := claims.aeroScopes(v.defaultScopes)
	if len(scopes) == 0 {
		return Key{}, errors.New("jwt: token has no recognized scopes")
	}
	return Key{
		Token: token, Tenant: tenant, SubjectID: claims.Sub,
		Roles:  append([]string(nil), claims.Roles...),
		Groups: append([]string(nil), claims.Groups...), Scopes: scopes,
	}, nil
}

func (v *JWKSVerifier) validateClaims(claims externalJWTClaims) error {
	if v.issuer != "" && claims.Iss != v.issuer {
		return fmt.Errorf("jwt: issuer mismatch: got %q, want %q", claims.Iss, v.issuer)
	}
	now := time.Now().Unix()
	if claims.Exp > 0 && now > claims.Exp {
		return errors.New("jwt: expired")
	}
	if claims.Nbf > 0 && now < claims.Nbf {
		return errors.New("jwt: not yet valid")
	}
	if v.audience != "" && !claims.matchesAudience(v.audience) {
		return errors.New("jwt: audience/client mismatch")
	}
	return nil
}

func (c externalJWTClaims) tenant(claim string) string {
	switch claim {
	case "sub":
		return c.Sub
	case "tenant_id":
		return c.TenantID
	default:
		return c.Ten
	}
}

func (c externalJWTClaims) matchesAudience(expected string) bool {
	if c.ClientID == expected || c.Azp == expected {
		return true
	}
	var single string
	if json.Unmarshal(c.Aud, &single) == nil && single == expected {
		return true
	}
	var many []string
	if json.Unmarshal(c.Aud, &many) != nil {
		return false
	}
	for _, audience := range many {
		if audience == expected {
			return true
		}
	}
	return false
}

func (c externalJWTClaims) aeroScopes(defaults []Scope) map[Scope]bool {
	resolved := map[Scope]bool{}
	values := append(append([]string(nil), c.Scopes...), strings.Fields(c.Scope)...)
	for _, value := range values {
		scope := Scope(value)
		if knownScope(scope) {
			resolved[scope] = true
		}
	}
	if len(resolved) == 0 {
		for _, scope := range defaults {
			if knownScope(scope) {
				resolved[scope] = true
			}
		}
	}
	return resolved
}
