package storage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// envelopeEncrypter wraps a master key and produces per-object envelopes:
//
//	{
//	  "alg":"AES-256-GCM",
//	  "kek":"base64",   // wrapped data key
//	  "iv":"base64"     // 96-bit nonce
//	}
//
// The data key encrypts the object body; the master key encrypts (KEK-wraps)
// the data key. Storing the data key with the object keeps the local backend
// self-contained; rotation involves rewriting envelopes.
type envelopeEncrypter struct {
	masterKey []byte // 32 bytes
}

func newEnvelopeEncrypter(passphrase string) (*envelopeEncrypter, error) {
	if passphrase == "" {
		return nil, errors.New("SSE master key (STORAGE_LOCAL_SSE_KEY) is required")
	}
	// Derive 32 bytes from the passphrase via HMAC-SHA256 — deterministic so
	// the same passphrase recovers data across restarts.
	mac := hmac.New(sha256.New, []byte("aero-vault/sse/v1"))
	mac.Write([]byte(passphrase))
	return &envelopeEncrypter{masterKey: mac.Sum(nil)}, nil
}

// encrypt produces (ciphertext, envelope-json). Envelope is small JSON suitable
// for sidecar storage.
func (e *envelopeEncrypter) encrypt(plain []byte) ([]byte, string, error) {
	dataKey := make([]byte, 32)
	if _, err := rand.Read(dataKey); err != nil {
		return nil, "", err
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return nil, "", err
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	ct := gcm.Seal(nil, iv, plain, nil)

	// Wrap the data key with the master key (AES-GCM as well, 12-byte nonce
	// prefixed to the ciphertext).
	mblock, err := aes.NewCipher(e.masterKey)
	if err != nil {
		return nil, "", err
	}
	mgcm, err := cipher.NewGCM(mblock)
	if err != nil {
		return nil, "", err
	}
	kekNonce := make([]byte, 12)
	if _, err := rand.Read(kekNonce); err != nil {
		return nil, "", err
	}
	wrapped := mgcm.Seal(kekNonce, kekNonce, dataKey, nil)

	env := fmt.Sprintf(`{"alg":"AES-256-GCM","kek":%q,"iv":%q}`,
		base64.StdEncoding.EncodeToString(wrapped),
		base64.StdEncoding.EncodeToString(iv),
	)
	return ct, env, nil
}

func (e *envelopeEncrypter) decrypt(ct []byte, envelope string) ([]byte, error) {
	wrapped, iv, err := parseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	mblock, err := aes.NewCipher(e.masterKey)
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
	dataKey, err := mgcm.Open(nil, wrapped[:12], wrapped[12:], nil)
	if err != nil {
		return nil, fmt.Errorf("unwrap data key: %w", err)
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

func parseEnvelope(s string) (kek, iv []byte, err error) {
	// Lightweight parse: find "kek":"…" and "iv":"…".
	kekStr := extractField(s, "kek")
	ivStr := extractField(s, "iv")
	if kekStr == "" || ivStr == "" {
		return nil, nil, errors.New("envelope: missing fields")
	}
	kek, err = base64.StdEncoding.DecodeString(kekStr)
	if err != nil {
		return nil, nil, err
	}
	iv, err = base64.StdEncoding.DecodeString(ivStr)
	return
}

func extractField(s, key string) string {
	needle := `"` + key + `":"`
	i := bytes.Index([]byte(s), []byte(needle))
	if i < 0 {
		return ""
	}
	start := i + len(needle)
	end := bytes.IndexByte([]byte(s)[start:], '"')
	if end < 0 {
		return ""
	}
	return s[start : start+end]
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
