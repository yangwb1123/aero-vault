package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// JWTVerifier validates HS256-signed tokens. Claims schema:
//
//	{
//	  "sub":   "<key id>",
//	  "ten":   "<tenant>",
//	  "scopes":["read","write",...],
//	  "exp":   1735689600
//	}
//
// We support HS256 only — it covers the common cases (internal SSO, sidecar
// auth proxies) without an asymmetric key dance. To roll forward to RS256/JWKS,
// swap NewJWTVerifier for an asymmetric implementation.
type JWTVerifier struct {
	secret []byte
	// expectedIssuer, when non-empty, requires the 'iss' claim to match. Opt-in
	// via WithIssuer so existing constructors stay source-compatible.
	expectedIssuer string
}

// WithIssuer pins the verifier to a single expected 'iss' claim. When set,
// Verify rejects tokens whose issuer differs, and Sign stamps the issuer onto
// freshly minted tokens. Chainable; a nil verifier is left untouched.
func (v *JWTVerifier) WithIssuer(iss string) *JWTVerifier {
	if v == nil {
		return nil
	}
	v.expectedIssuer = iss
	return v
}

func NewJWTVerifier(secret string) *JWTVerifier {
	if secret == "" {
		return nil
	}
	return &JWTVerifier{secret: []byte(secret)}
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Sub    string   `json:"sub,omitempty"`
	Ten    string   `json:"ten,omitempty"`
	Iss    string   `json:"iss,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
	Roles  []string `json:"roles,omitempty"`
	Groups []string `json:"groups,omitempty"`
	Exp    int64    `json:"exp,omitempty"`
	Nbf    int64    `json:"nbf,omitempty"`
}

func verifySignature(token string, parts []string, secret []byte) error {
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return errors.New("jwt: bad signature")
	}
	return nil
}

func decodeJWTHeader(part string) (jwtHeader, error) {
	headerBytes, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return jwtHeader{}, err
	}
	var h jwtHeader
	if err := json.Unmarshal(headerBytes, &h); err != nil {
		return jwtHeader{}, err
	}
	if strings.ToUpper(h.Alg) != "HS256" {
		return jwtHeader{}, errors.New("jwt: unsupported alg " + h.Alg)
	}
	return h, nil
}

func (v *JWTVerifier) decodeAndValidateClaims(part string) (jwtClaims, error) {
	claimsBytes, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return jwtClaims{}, err
	}
	var c jwtClaims
	if err := json.Unmarshal(claimsBytes, &c); err != nil {
		return jwtClaims{}, err
	}
	if v.expectedIssuer != "" && c.Iss != v.expectedIssuer {
		return jwtClaims{}, errors.New("jwt: issuer mismatch")
	}
	now := time.Now().Unix()
	if c.Exp > 0 && now > c.Exp {
		return jwtClaims{}, errors.New("jwt: expired")
	}
	if c.Nbf > 0 && now < c.Nbf {
		return jwtClaims{}, errors.New("jwt: not yet valid")
	}
	// A missing tenant must not silently become "*" (the admin wildcard that
	// bypasses tenant pinning in middleware) — that would grant cross-tenant
	// admin access to any token lacking a 'ten' claim.  Require it explicitly.
	if c.Ten == "" {
		return jwtClaims{}, errors.New("jwt: missing or empty tenant claim")
	}
	return c, nil
}

func (v *JWTVerifier) decodeValidateClaims(token string, parts []string) (jwtClaims, error) {
	if _, err := decodeJWTHeader(parts[0]); err != nil {
		return jwtClaims{}, err
	}
	return v.decodeAndValidateClaims(parts[1])
}

func claimsToKey(token string, c jwtClaims) (Key, error) {
	k := Key{
		Token: token, Tenant: c.Ten, SubjectID: c.Sub,
		Roles: append([]string(nil), c.Roles...), Groups: append([]string(nil), c.Groups...),
		Scopes: map[Scope]bool{},
	}
	for _, s := range c.Scopes {
		k.Scopes[Scope(s)] = true
	}
	if len(k.Scopes) == 0 {
		return Key{}, errors.New("jwt: token has no scopes")
	}
	return k, nil
}

// Verify parses, checks the signature, expiry, and returns a Key shaped like
// the API-key path so the Middleware code stays uniform.
func (v *JWTVerifier) Verify(token string) (Key, error) {
	if v == nil {
		return Key{}, errors.New("jwt verifier not configured")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Key{}, errors.New("jwt: not a 3-part token")
	}
	if err := verifySignature(token, parts, v.secret); err != nil {
		return Key{}, err
	}
	c, err := v.decodeValidateClaims(token, parts)
	if err != nil {
		return Key{}, err
	}
	return claimsToKey(token, c)
}

// Sign creates a token for testing / Admin API key issuance. Production
// callers usually issue tokens out-of-band via their IdP.
func (v *JWTVerifier) Sign(c struct {
	Sub    string
	Tenant string
	Scopes []string
	TTL    time.Duration
}) (string, error) {
	return v.SignWithPrincipal(JWTSignClaims{
		Sub: c.Sub, Tenant: c.Tenant, Scopes: c.Scopes, TTL: c.TTL,
	})
}

type JWTSignClaims struct {
	Sub    string
	Tenant string
	Scopes []string
	Roles  []string
	Groups []string
	TTL    time.Duration
}

// SignWithPrincipal issues a local token carrying Aero's normalized role and
// group claims in addition to the legacy subject, tenant, and scope claims.
func (v *JWTVerifier) SignWithPrincipal(c JWTSignClaims) (string, error) {
	if v == nil {
		return "", errors.New("jwt verifier not configured")
	}
	header := jwtHeader{Alg: "HS256", Typ: "JWT"}
	hb, _ := json.Marshal(header)
	now := time.Now().Unix()
	claims := jwtClaims{
		Sub: c.Sub, Ten: c.Tenant, Iss: v.expectedIssuer, Scopes: c.Scopes,
		Roles: c.Roles, Groups: c.Groups, Nbf: now,
	}
	if c.TTL > 0 {
		claims.Exp = now + int64(c.TTL/time.Second)
	}
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig, nil
}
