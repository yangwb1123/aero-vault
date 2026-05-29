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
	Scopes []string `json:"scopes,omitempty"`
	Exp    int64    `json:"exp,omitempty"`
	Nbf    int64    `json:"nbf,omitempty"`
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
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(signingInput))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return Key{}, errors.New("jwt: bad signature")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Key{}, err
	}
	var h jwtHeader
	if err := json.Unmarshal(headerBytes, &h); err != nil {
		return Key{}, err
	}
	if strings.ToUpper(h.Alg) != "HS256" {
		return Key{}, errors.New("jwt: unsupported alg " + h.Alg)
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Key{}, err
	}
	var c jwtClaims
	if err := json.Unmarshal(claimsBytes, &c); err != nil {
		return Key{}, err
	}
	now := time.Now().Unix()
	if c.Exp > 0 && now > c.Exp {
		return Key{}, errors.New("jwt: expired")
	}
	if c.Nbf > 0 && now < c.Nbf {
		return Key{}, errors.New("jwt: not yet valid")
	}
	k := Key{Token: token, Tenant: c.Ten, Scopes: map[Scope]bool{}}
	if k.Tenant == "" {
		k.Tenant = "*"
	}
	for _, s := range c.Scopes {
		k.Scopes[Scope(s)] = true
	}
	return k, nil
}

// Sign creates a token for testing / Admin API key issuance. Production
// callers usually issue tokens out-of-band via their IdP.
func (v *JWTVerifier) Sign(c struct {
	Sub    string
	Tenant string
	Scopes []string
	TTL    time.Duration
}) (string, error) {
	if v == nil {
		return "", errors.New("jwt verifier not configured")
	}
	header := jwtHeader{Alg: "HS256", Typ: "JWT"}
	hb, _ := json.Marshal(header)
	now := time.Now().Unix()
	claims := jwtClaims{Sub: c.Sub, Ten: c.Tenant, Scopes: c.Scopes, Nbf: now}
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
