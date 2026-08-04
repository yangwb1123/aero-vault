package auth

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// JWKSProvider fetches RSA and Ed25519 keys from a JWKS endpoint and caches
// them for key rotation with configurable TTL.
type JWKSProvider struct {
	url       string
	client    *http.Client
	mu        sync.RWMutex
	keys      map[string]jwksPublicKey
	expiresAt time.Time
	ttl       time.Duration
}

type jwksPublicKey struct {
	alg string
	key crypto.PublicKey
}

// NewJWKSProvider creates a provider that fetches keys from a JWKS URL.
// Keys are cached for ttl duration and refreshed on cache miss.
func NewJWKSProvider(url string, ttl time.Duration) *JWKSProvider {
	return &JWKSProvider{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
		keys:   make(map[string]jwksPublicKey),
		ttl:    ttl,
	}
}

// GetKey returns the public key for kid, refreshing an expired cache first.
func (p *JWKSProvider) GetKey(kid string) (jwksPublicKey, error) {
	p.mu.RLock()
	key, ok := p.keys[kid]
	expired := time.Now().After(p.expiresAt)
	p.mu.RUnlock()
	if ok && !expired {
		return key, nil
	}
	return p.refreshAndGet(kid)
}

func (p *JWKSProvider) refreshAndGet(kid string) (jwksPublicKey, error) {
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
		return jwksPublicKey{}, fmt.Errorf("jwks fetch: %w", err)
	}
	key, ok := p.keys[kid]
	if !ok {
		return jwksPublicKey{}, fmt.Errorf("jwks: key %q not found in key set", kid)
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
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
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
	keys := make(map[string]jwksPublicKey, len(jwks.Keys))
	for _, jk := range jwks.Keys {
		if jk.Use != "" && jk.Use != "sig" {
			continue
		}
		key, material, err := publicKeyFromJWK(jk)
		if err != nil {
			continue // skip malformed keys
		}
		kid := jk.Kid
		if kid == "" {
			kid = keyIDFromMaterial(material)
		}
		keys[kid] = jwksPublicKey{alg: jk.Alg, key: key}
	}
	p.keys = keys
	p.expiresAt = time.Now().Add(p.ttl)
	return nil
}

func publicKeyFromJWK(jk jwksKey) (crypto.PublicKey, string, error) {
	switch jk.Kty {
	case "RSA":
		key, err := rsaPublicKeyFromJWK(jk.N, jk.E)
		return key, jk.N, err
	case "OKP":
		if jk.Crv != "Ed25519" {
			return nil, "", fmt.Errorf("jwks: unsupported OKP curve %q", jk.Crv)
		}
		raw, err := base64.RawURLEncoding.DecodeString(jk.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, "", fmt.Errorf("jwks: malformed Ed25519 key")
		}
		return ed25519.PublicKey(raw), jk.X, nil
	default:
		return nil, "", fmt.Errorf("jwks: unsupported key type %q", jk.Kty)
	}
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

// keyIDFromMaterial derives a stable key ID when a JWK omits kid.
func keyIDFromMaterial(material string) string {
	h := sha256.Sum256([]byte(material))
	return fmt.Sprintf("auto-%x", h[:8])
}
