package auth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JWKSProvider fetches RSA public keys from a JWKS (JSON Web Key Set) endpoint
// and caches them for key rotation with configurable TTL. Used by JWTVerifier
// to validate RS256-signed tokens from external identity providers.
type JWKSProvider struct {
	url       string
	client    *http.Client
	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	expiresAt time.Time
	ttl       time.Duration
}

// NewJWKSProvider creates a provider that fetches keys from a JWKS URL.
// Keys are cached for ttl duration and refreshed on cache miss.
func NewJWKSProvider(url string, ttl time.Duration) *JWKSProvider {
	return &JWKSProvider{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
		keys:   make(map[string]*rsa.PublicKey),
		ttl:    ttl,
	}
}

// GetKey returns the RSA public key for the given key ID (kid). If the key is
// not in the cache or the cache has expired, it refreshes from the JWKS endpoint.
func (p *JWKSProvider) GetKey(kid string) (*rsa.PublicKey, error) {
	p.mu.RLock()
	key, ok := p.keys[kid]
	expired := time.Now().After(p.expiresAt)
	p.mu.RUnlock()
	if ok && !expired {
		return key, nil
	}
	return p.refreshAndGet(kid)
}

func (p *JWKSProvider) refreshAndGet(kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Double-check after acquiring write lock.
	if key, ok := p.keys[kid]; ok && time.Now().Before(p.expiresAt) {
		return key, nil
	}
	if err := p.fetch(); err != nil {
		// If we have cached keys, serve from cache even if expired rather than failing.
		if key, ok := p.keys[kid]; ok {
			return key, nil
		}
		return nil, fmt.Errorf("jwks fetch: %w", err)
	}
	key, ok := p.keys[kid]
	if !ok {
		return nil, fmt.Errorf("jwks: key %q not found in key set", kid)
	}
	return key, nil
}

// jwksResponse is the JSON structure returned by a JWKS endpoint.
type jwksResponse struct {
	Keys []jwksKey `json:"keys"`
}

type jwksKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use,omitempty"`
	N   string `json:"n"` // modulus (base64url)
	E   string `json:"e"` // exponent (base64url)
	Alg string `json:"alg,omitempty"`
}

func (p *JWKSProvider) fetch() error {
	resp, err := p.client.Get(p.url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks: HTTP %d", resp.StatusCode)
	}
	var jwks jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("jwks: decode: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, jk := range jwks.Keys {
		if jk.Kty != "RSA" {
			continue
		}
		key, err := rsaPublicKeyFromJWK(jk.N, jk.E)
		if err != nil {
			continue // skip malformed keys
		}
		kid := jk.Kid
		if kid == "" {
			kid = keyIDFromModulus(jk.N)
		}
		keys[kid] = key
	}
	p.keys = keys
	p.expiresAt = time.Now().Add(p.ttl)
	return nil
}

// rsaPublicKeyFromJWK converts base64url-encoded modulus (n) and exponent (e)
// to an *rsa.PublicKey.
func rsaPublicKeyFromJWK(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	if len(eBytes) < 4 {
		padded := make([]byte, 4)
		copy(padded[4-len(eBytes):], eBytes)
		eBytes = padded
	}
	n := new(big.Int).SetBytes(nBytes)
	e := int(binary.BigEndian.Uint32(eBytes))
	return &rsa.PublicKey{N: n, E: e}, nil
}

// keyIDFromModulus derives a stable key ID from the modulus (used when JWKS
// keys don't include a "kid" field).
func keyIDFromModulus(nB64 string) string {
	h := sha256.Sum256([]byte(nB64))
	return fmt.Sprintf("auto-%x", h[:8])
}

// RS256Verifier validates RS256-signed JWT tokens using a JWKS provider.
type RS256Verifier struct {
	provider *JWKSProvider
	issuer   string
}

// NewRS256Verifier creates a verifier that validates RS256 tokens using keys
// fetched from the JWKS endpoint.
func NewRS256Verifier(jwksURL string, jwksTTL time.Duration, issuer string) *RS256Verifier {
	return &RS256Verifier{
		provider: NewJWKSProvider(jwksURL, jwksTTL),
		issuer:   issuer,
	}
}

// Verify parses an RS256 JWT, fetches the key from JWKS, and validates the
// signature and claims. Returns a Key compatible with the auth middleware.
func (v *RS256Verifier) Verify(token string) (Key, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Key{}, errors.New("jwt: not a 3-part token")
	}
	// Decode header to get kid.
	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Key{}, fmt.Errorf("jwt: decode header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid,omitempty"`
	}
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		return Key{}, fmt.Errorf("jwt: parse header: %w", err)
	}
	if strings.ToUpper(hdr.Alg) != "RS256" {
		return Key{}, fmt.Errorf("jwt: unsupported alg %q (expected RS256)", hdr.Alg)
	}
	if hdr.Kid == "" {
		return Key{}, errors.New("jwt: RS256 token must have 'kid' header")
	}
	// Get the public key.
	pubKey, err := v.provider.GetKey(hdr.Kid)
	if err != nil {
		return Key{}, fmt.Errorf("jwt: get key: %w", err)
	}
	// Verify signature.
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Key{}, fmt.Errorf("jwt: decode sig: %w", err)
	}
	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, hash[:], sig); err != nil {
		return Key{}, errors.New("jwt: bad signature")
	}
	// Decode and validate claims.
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Key{}, fmt.Errorf("jwt: decode claims: %w", err)
	}
	var claims struct {
		Sub    string   `json:"sub,omitempty"`
		Ten    string   `json:"ten,omitempty"`
		Iss    string   `json:"iss,omitempty"`
		Scopes []string `json:"scopes,omitempty"`
		Exp    int64    `json:"exp,omitempty"`
		Nbf    int64    `json:"nbf,omitempty"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return Key{}, fmt.Errorf("jwt: parse claims: %w", err)
	}
	if v.issuer != "" && claims.Iss != v.issuer {
		return Key{}, fmt.Errorf("jwt: issuer mismatch: got %q, want %q", claims.Iss, v.issuer)
	}
	now := time.Now().Unix()
	if claims.Exp > 0 && now > claims.Exp {
		return Key{}, errors.New("jwt: expired")
	}
	if claims.Nbf > 0 && now < claims.Nbf {
		return Key{}, errors.New("jwt: not yet valid")
	}
	if claims.Ten == "" {
		return Key{}, errors.New("jwt: missing tenant claim")
	}
	k := Key{Token: token, Tenant: claims.Ten, Scopes: map[Scope]bool{}}
	for _, s := range claims.Scopes {
		k.Scopes[Scope(s)] = true
	}
	if len(k.Scopes) == 0 {
		return Key{}, errors.New("jwt: token has no scopes")
	}
	return k, nil
}
