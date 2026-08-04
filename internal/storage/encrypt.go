package storage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// sseAlg is the only algorithm this envelope format supports. Recorded on write
// and verified on read so a tampered/foreign envelope can't coax a weaker scheme.
const sseAlg = "AES-256-GCM"

// masterKeyLen is the required master-key length (AES-256). External
// SecretProvider implementations that return a different size are rejected rather
// than silently downgrading to AES-128/192.
const masterKeyLen = 32

// sseEnvelope is the per-object SSE envelope, stored as sidecar JSON. Marshaled
// and unmarshaled with encoding/json so key ids and base64 fields are correctly
// escaped (a hand-rolled parser mishandles escaped quotes). With an empty Kid the
// field is omitted, keeping the single-key env provider byte-compatible with
// pre-rotation envelopes.
type sseEnvelope struct {
	Alg  string `json:"alg"`
	Wrap string `json:"wrap,omitempty"` // "" = local master-key wrapping; "kms" = remote DataKeyWrapper
	Kid  string `json:"kid,omitempty"`
	Kek  string `json:"kek"` // base64, wrapped data key
	IV   string `json:"iv"`  // base64, 96-bit object nonce
}

// envelopeEncrypter produces per-object envelopes using master keys supplied by a
// SecretProvider:
//
//	{
//	  "alg":"AES-256-GCM",
//	  "kid":"2026-06",  // master key id (omitted for the single-key env provider)
//	  "kek":"base64",   // wrapped data key
//	  "iv":"base64"     // 96-bit nonce
//	}
//
// The data key encrypts the object body; a master key KEK-wraps the data key. The
// master key's id is recorded as "kid" so the key can rotate without rewriting
// existing envelopes — on read the recorded id is resolved back to its key.
type envelopeEncrypter struct {
	provider SecretProvider // local master-key wrapping; nil when a remote wrapper is used
	wrapper  DataKeyWrapper // remote wrapping (e.g. KMS); nil when a provider is used
}

func newEnvelopeEncrypter(provider SecretProvider) *envelopeEncrypter {
	return &envelopeEncrypter{provider: provider}
}

// newWrappingEncrypter builds an encrypter that wraps each data key remotely via
// w (e.g. a KMS), so the wrapping key never reaches this process.
func newWrappingEncrypter(w DataKeyWrapper) *envelopeEncrypter {
	return &envelopeEncrypter{wrapper: w}
}

func generateDataKey() (key, nonce []byte, err error) {
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return key, nonce, nil
}

func gcmSeal(plaintext, dataKey, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, nil), nil
}

// encrypt produces (ciphertext, envelope-json). Envelope is small JSON suitable
// for sidecar storage.
func (e *envelopeEncrypter) encrypt(plain []byte) ([]byte, string, error) {
	dataKey, iv, err := generateDataKey()
	if err != nil {
		return nil, "", err
	}
	ct, err := gcmSeal(plain, dataKey, iv)
	if err != nil {
		return nil, "", err
	}

	// Remote wrapping (KMS): the wrapping key never leaves the wrapper.
	if e.wrapper != nil {
		wrapped, keyID, err := e.wrapper.WrapKey(dataKey)
		if err != nil {
			return nil, "", fmt.Errorf("sse: kms wrap: %w", err)
		}
		env, err := buildEnvelope("kms", keyID, wrapped, iv)
		if err != nil {
			return nil, "", err
		}
		return ct, env, nil
	}

	// Local KEK-wrap the data key with the provider's current master key and record
	// its id in the envelope so rotation never has to rewrite this object.
	keyID, masterKey := e.provider.Current()
	if len(masterKey) != masterKeyLen {
		return nil, "", fmt.Errorf("sse: master key for %q is %d bytes, want %d", keyID, len(masterKey), masterKeyLen)
	}
	wrapped, err := wrapKey(masterKey, dataKey)
	if err != nil {
		return nil, "", err
	}
	env, err := buildEnvelope("", keyID, wrapped, iv)
	if err != nil {
		return nil, "", err
	}
	return ct, env, nil
}

func (e *envelopeEncrypter) decrypt(ct []byte, envelope string) ([]byte, error) {
	env, err := parseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	if env.Alg != sseAlg {
		return nil, fmt.Errorf("sse: unsupported envelope alg %q", env.Alg)
	}
	wrapped, err := base64.StdEncoding.DecodeString(env.Kek)
	if err != nil {
		return nil, fmt.Errorf("sse: decode kek: %w", err)
	}
	iv, err := base64.StdEncoding.DecodeString(env.IV)
	if err != nil {
		return nil, fmt.Errorf("sse: decode iv: %w", err)
	}
	if len(iv) != 12 {
		return nil, fmt.Errorf("sse: envelope iv is %d bytes, want 12", len(iv))
	}

	dataKey, err := e.unwrapDataKey(env, wrapped)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, iv, ct, nil)
}

func (e *envelopeEncrypter) validateEnvelope(envelope string) error {
	env, err := parseEnvelope(envelope)
	if err != nil {
		return err
	}
	if env.Alg != sseAlg {
		return fmt.Errorf("sse: unsupported envelope alg %q", env.Alg)
	}
	wrapped, err := base64.StdEncoding.DecodeString(env.Kek)
	if err != nil {
		return fmt.Errorf("sse: decode kek: %w", err)
	}
	_, err = e.unwrapDataKey(env, wrapped)
	return err
}

// unwrapDataKey recovers the per-object data key from an envelope, dispatching on
// the wrap mode: remote (KMS) via the wrapper, or local via the master-key
// provider. Each path guards that the matching mechanism is configured, so a
// wrapper-only encrypter never mishandles a local envelope (and vice versa).
func (e *envelopeEncrypter) unwrapDataKey(env sseEnvelope, wrapped []byte) ([]byte, error) {
	switch env.Wrap {
	case "kms":
		if e.wrapper == nil {
			return nil, errors.New("sse: object uses KMS wrapping but no KMS is configured")
		}
		dataKey, err := e.wrapper.UnwrapKey(wrapped, env.Kid)
		if err != nil {
			return nil, fmt.Errorf("sse: kms unwrap: %w", err)
		}
		return dataKey, nil
	case "":
		if e.provider == nil {
			return nil, errors.New("sse: object uses local wrapping but no key provider is configured")
		}
		masterKey, ok := e.provider.Resolve(env.Kid)
		if !ok {
			if env.Kid == "" {
				return nil, errors.New("sse: object has no key id and no legacy key is configured")
			}
			return nil, fmt.Errorf("sse: unknown key version %q", env.Kid)
		}
		if len(masterKey) != masterKeyLen {
			return nil, fmt.Errorf("sse: master key for %q is %d bytes, want %d", env.Kid, len(masterKey), masterKeyLen)
		}
		dataKey, err := unwrapKey(masterKey, wrapped)
		if err != nil {
			return nil, fmt.Errorf("unwrap data key: %w", err)
		}
		return dataKey, nil
	default:
		return nil, fmt.Errorf("sse: unsupported wrap mode %q", env.Wrap)
	}
}

// rewrap re-wraps an existing envelope's data key under the provider's CURRENT
// master key, leaving the object body (and its data key + iv) untouched — only the
// wrapped key (kek) and kid change. This is how rotation migrates an object to a
// new master key without rewriting its body. Returns (newEnvelope, changed);
// changed is false when the envelope already uses the current key.
func (e *envelopeEncrypter) rewrap(envelope string) (string, bool, error) {
	env, err := parseEnvelope(envelope)
	if err != nil {
		return "", false, err
	}
	if env.Alg != sseAlg {
		return "", false, fmt.Errorf("sse: unsupported envelope alg %q", env.Alg)
	}
	if env.Wrap != "" {
		return envelope, false, nil
	}
	curID, curKey := e.provider.Current()
	if env.Kid == curID {
		return envelope, false, nil
	}
	oldKey, err := e.resolveOldKey(env.Kid)
	if err != nil {
		return "", false, err
	}
	if len(curKey) != masterKeyLen {
		return "", false, fmt.Errorf("sse: master key must be %d bytes", masterKeyLen)
	}
	newEnv, err := rewrapObject(env, oldKey, curKey, curID)
	if err != nil {
		return "", false, err
	}
	return newEnv, true, nil
}

func (e *envelopeEncrypter) resolveOldKey(kid string) ([]byte, error) {
	oldKey, ok := e.provider.Resolve(kid)
	if !ok {
		if kid == "" {
			return nil, errors.New("sse: object has no key id and no legacy key is configured")
		}
		return nil, fmt.Errorf("sse: unknown key version %q", kid)
	}
	if len(oldKey) != masterKeyLen {
		return nil, fmt.Errorf("sse: master key for %q is %d bytes, want %d", kid, len(oldKey), masterKeyLen)
	}
	return oldKey, nil
}

func rewrapObject(env sseEnvelope, oldKey, curKey []byte, curID string) (string, error) {
	wrapped, err := base64.StdEncoding.DecodeString(env.Kek)
	if err != nil {
		return "", fmt.Errorf("sse: decode kek: %w", err)
	}
	iv, err := base64.StdEncoding.DecodeString(env.IV)
	if err != nil {
		return "", fmt.Errorf("sse: decode iv: %w", err)
	}
	if len(iv) != 12 {
		return "", fmt.Errorf("sse: envelope iv is %d bytes, want 12", len(iv))
	}
	dataKey, err := unwrapKey(oldKey, wrapped)
	if err != nil {
		return "", fmt.Errorf("rewrap unwrap: %w", err)
	}
	newWrapped, err := wrapKey(curKey, dataKey)
	if err != nil {
		return "", err
	}
	return buildEnvelope("", curID, newWrapped, iv)
}

// wrapKey KEK-wraps a data key with a master key (AES-GCM, 12-byte nonce prefixed
// to the ciphertext).
func wrapKey(masterKey, dataKey []byte) ([]byte, error) {
	mblock, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	mgcm, err := cipher.NewGCM(mblock)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return mgcm.Seal(nonce, nonce, dataKey, nil), nil
}

func unwrapKey(masterKey, wrapped []byte) ([]byte, error) {
	mblock, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	mgcm, err := cipher.NewGCM(mblock)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < 12 {
		return nil, errors.New("envelope: wrapped key too short")
	}
	return mgcm.Open(nil, wrapped[:12], wrapped[12:], nil)
}

// buildEnvelope renders the envelope JSON via encoding/json. An empty wrap and
// keyID omit those fields (omitempty), keeping a single-key env-provider envelope
// byte-compatible with the pre-rotation, pre-KMS format.
func buildEnvelope(wrap, keyID string, wrapped, iv []byte) (string, error) {
	b, err := json.Marshal(sseEnvelope{
		Alg:  sseAlg,
		Wrap: wrap,
		Kid:  keyID,
		Kek:  base64.StdEncoding.EncodeToString(wrapped),
		IV:   base64.StdEncoding.EncodeToString(iv),
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseEnvelope(s string) (sseEnvelope, error) {
	var env sseEnvelope
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		return env, fmt.Errorf("sse: malformed envelope: %w", err)
	}
	if env.Kek == "" || env.IV == "" {
		return env, errors.New("sse: envelope missing kek/iv")
	}
	return env, nil
}

// io.Reader wrappers for encrypt/decrypt-on-the-fly. Because GCM requires the
// whole ciphertext to verify the tag, we buffer through []byte here. This is
// fine for objects up to ~hundreds of MB; for streaming SSE on huge files,
// swap in AES-CTR + HMAC chunked.
func encryptReader(r io.Reader, enc *envelopeEncrypter) (io.Reader, string, error) {
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	ct, env, err := enc.encrypt(plain)
	if err != nil {
		return nil, "", err
	}
	return bytes.NewReader(ct), env, nil
}

func decryptReader(r io.Reader, envelope string, enc *envelopeEncrypter) (io.ReadCloser, error) {
	ct, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	pt, err := enc.decrypt(ct, envelope)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(pt)), nil
}
