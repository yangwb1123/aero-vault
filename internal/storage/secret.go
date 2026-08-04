package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"
)

// keyIDPattern constrains key ids to characters that round-trip safely and read
// cleanly in logs/envelopes. Rejecting anything else at load time keeps exotic ids
// out of the envelope entirely.
var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// SecretProvider supplies versioned master keys for envelope SSE. The active key
// (Current) KEK-wraps each object's data key on write and its id is recorded in
// the object envelope; on read the recorded id is resolved back to its key. This
// lets the master key rotate without rewriting existing envelopes: old objects
// decrypt under their original key version, new writes use the current one.
//
// Built-in providers (env, keyfile, http) are constructed from LocalConfig. Other
// key sources (a bespoke KMS client, age, …) can implement this interface and be
// supplied via LocalConfig.Secrets.
type SecretProvider interface {
	// Current returns the active key id and its 32-byte master key for new
	// writes. An empty id means "stamp no key id" — used by the single-key env
	// provider so its envelopes stay byte-identical to the pre-rotation format.
	Current() (id string, key []byte)
	// Resolve returns the 32-byte master key for a recorded envelope key id. The
	// id "" is the legacy slot (objects written before key versioning). ok is
	// false when the id is unknown — callers must treat that as a hard error and
	// never silently fall back to another key.
	Resolve(id string) (key []byte, ok bool)
}

// DataKeyWrapper wraps/unwraps a per-object data key with a key that never leaves
// the wrapper's custody (e.g. a KMS). Unlike SecretProvider — which hands back
// master key material for local wrapping — the wrapping key stays remote: only the
// wrap and unwrap operations cross the boundary. Implementations must be safe for
// concurrent use and apply their own timeouts. Supplied via LocalConfig.KMS (or
// STORAGE_LOCAL_SSE_KMS_URL); it takes precedence over the SecretProvider.
type DataKeyWrapper interface {
	// WrapKey wraps a 32-byte data key, returning the wrapped form and the id of
	// the key that wrapped it (recorded in the envelope so UnwrapKey can target it).
	WrapKey(dataKey []byte) (wrapped []byte, keyID string, err error)
	// UnwrapKey reverses WrapKey for the recorded keyID.
	UnwrapKey(wrapped []byte, keyID string) (dataKey []byte, err error)
}

// deriveSSEKey turns a passphrase into a deterministic 32-byte master key via
// HMAC-SHA256 over a fixed label, so the same passphrase always recovers the same
// data across restarts and across providers. (Unchanged from the original
// single-key derivation, preserving backward compatibility with existing objects.)
func deriveSSEKey(passphrase string) []byte {
	mac := hmac.New(sha256.New, []byte("aero-vault/sse/v1"))
	mac.Write([]byte(passphrase))
	return mac.Sum(nil)
}

// envProvider is the default single-key provider: one passphrase, no rotation. It
// stamps no key id (Current returns ""), so its envelopes are identical to the
// format used before key versioning existed — a zero-migration default.
type envProvider struct{ key []byte }

func newEnvProvider(passphrase string) (*envProvider, error) {
	if passphrase == "" {
		return nil, errors.New("SSE master key (STORAGE_LOCAL_SSE_KEY) is required")
	}
	return &envProvider{key: deriveSSEKey(passphrase)}, nil
}

func (p *envProvider) Current() (string, []byte) { return "", p.key }

func (p *envProvider) Resolve(id string) ([]byte, bool) {
	if id == "" {
		return p.key, true
	}
	return nil, false
}

// keyRingProvider holds multiple versioned master keys and a pointer to the
// primary (current) one. Rotating a key is adding a new entry and moving
// "primary"; existing objects keep decrypting under their recorded id. The ring
// is loaded once at construction from a JSON document of the shape:
//
//	{"primary":"2026-06","keys":{"2026-06":"<passphrase>","2025-01":"<passphrase>"}}
//
// sourced from a local file (keyfile) or an HTTP secret store (http, e.g. Vault
// KV). An optional legacy passphrase (typically the former STORAGE_LOCAL_SSE_KEY)
// decrypts pre-versioning, no-id envelopes.
type keyRingProvider struct {
	primary string
	keys    map[string][]byte // id -> 32-byte master key
	legacy  []byte            // key for no-id envelopes; nil if none configured
}

type keyRingDoc struct {
	Primary string            `json:"primary"`
	Keys    map[string]string `json:"keys"`
}

// newKeyRing parses, validates and derives a key ring from its JSON form.
func newKeyRing(raw []byte, legacyPassphrase string) (*keyRingProvider, error) {
	var doc keyRingDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse sse key ring: %w", err)
	}
	if len(doc.Keys) == 0 {
		return nil, errors.New("sse key ring: no keys defined")
	}
	if doc.Primary == "" {
		return nil, errors.New("sse key ring: primary is required")
	}
	if _, ok := doc.Keys[doc.Primary]; !ok {
		return nil, fmt.Errorf("sse key ring: primary %q is not in keys", doc.Primary)
	}
	keys := make(map[string][]byte, len(doc.Keys))
	for id, pass := range doc.Keys {
		if id == "" {
			return nil, errors.New(`sse key ring: key id "" is reserved`)
		}
		if !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("sse key ring: key id %q has invalid characters (allowed: letters, digits, . _ -)", id)
		}
		if pass == "" {
			return nil, fmt.Errorf("sse key ring: key %q has an empty passphrase", id)
		}
		keys[id] = deriveSSEKey(pass)
	}
	p := &keyRingProvider{primary: doc.Primary, keys: keys}
	if legacyPassphrase != "" {
		p.legacy = deriveSSEKey(legacyPassphrase)
	}
	return p, nil
}

// newKeyfileProvider loads a key ring from a local JSON file.
func newKeyfileProvider(path, legacyPassphrase string) (*keyRingProvider, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sse keyfile: %w", err)
	}
	return newKeyRing(raw, legacyPassphrase)
}

// newHTTPProvider loads a key ring from an HTTP secret store (e.g. Vault KV) at
// startup. token, when set, is sent as a Bearer credential. The ring is fetched
// once; rotation is picked up on the next restart.
func newHTTPProvider(url, token, legacyPassphrase string) (*keyRingProvider, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("sse key url: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("sse key url fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("sse key url http %d: %s", resp.StatusCode, string(b))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // cap at 1 MiB
	if err != nil {
		return nil, fmt.Errorf("sse key url read: %w", err)
	}
	return newKeyRing(raw, legacyPassphrase)
}

func (p *keyRingProvider) Current() (string, []byte) {
	return p.primary, p.keys[p.primary]
}

func (p *keyRingProvider) Resolve(id string) ([]byte, bool) {
	if id == "" {
		if p.legacy == nil {
			return nil, false
		}
		return p.legacy, true
	}
	k, ok := p.keys[id]
	return k, ok
}

// newDataKeyWrapper selects a remote wrapper from LocalConfig: an explicit KMS
// wrapper wins, else an HTTP KMS endpoint, else nil (no remote wrapping → fall
// back to a local SecretProvider).
func newDataKeyWrapper(cfg LocalConfig) DataKeyWrapper {
	switch {
	case cfg.KMS != nil:
		return cfg.KMS
	case cfg.SSEKMSURL != "":
		return newHTTPKMS(cfg.SSEKMSURL, cfg.SSEKMSKeyID, cfg.SSEKMSToken)
	default:
		return nil
	}
}

// newSecretProvider selects a provider from LocalConfig. Precedence: an explicit
// Secrets provider (e.g. a custom KMS client) wins; else an HTTP secret store
// (Vault KV); else a local keyfile; else a single env passphrase; else nil (SSE
// disabled). The keyed providers take the env key as their legacy slot.
const ssecKeyID = "__ssec__"

// ssecProvider is a request-scoped key source for SSE-C.
type ssecProvider struct {
	key []byte
}

func newSSECProvider(key []byte) *ssecProvider {
	return &ssecProvider{key: append([]byte(nil), key...)}
}

func (p *ssecProvider) Current() (string, []byte) { return ssecKeyID, p.key }
func (p *ssecProvider) Resolve(id string) ([]byte, bool) {
	if id == ssecKeyID {
		return p.key, true
	}
	return nil, false
}

func newSecretProvider(cfg LocalConfig) (SecretProvider, error) {
	switch {
	case cfg.Secrets != nil:
		return cfg.Secrets, nil
	case cfg.SSEKeyURL != "":
		return newHTTPProvider(cfg.SSEKeyURL, cfg.SSEKeyToken, cfg.SSEKey)
	case cfg.SSEKeyfile != "":
		return newKeyfileProvider(cfg.SSEKeyfile, cfg.SSEKey)
	case cfg.SSEKey != "":
		return newEnvProvider(cfg.SSEKey)
	default:
		return nil, nil
	}
}
