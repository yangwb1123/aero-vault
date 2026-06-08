package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestLocalSSERoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir, SSEKey: "test-master-passphrase"})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	body := []byte("this is the secret payload\n")
	ctx := context.Background()
	if _, err := s.Put(ctx, "default/secret.txt", bytes.NewReader(body), int64(len(body)), PutOptions{ContentType: "text/plain"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, _, err := s.Get(ctx, "default/secret.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("roundtrip mismatch: %q", string(got))
	}
}

// The JSON envelope round-trips key ids that the old hand-rolled parser would
// mangle (quotes/backslashes/control chars).
func TestEnvelope_RoundTripEscaping(t *testing.T) {
	tricky := "weird\"kid\\with\tchars"
	env, err := buildEnvelope("", tricky, []byte("wrapped-key-bytes-............"), make([]byte, 12))
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kid != tricky {
		t.Fatalf("kid round-trip broken: %q != %q", got.Kid, tricky)
	}
	if got.Alg != sseAlg {
		t.Fatalf("alg: %q", got.Alg)
	}
}

func mustEncrypt(t *testing.T) (*envelopeEncrypter, []byte, string) {
	t.Helper()
	p, err := newEnvProvider("pw")
	if err != nil {
		t.Fatal(err)
	}
	enc := newEnvelopeEncrypter(p)
	ct, env, err := enc.encrypt([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	return enc, ct, env
}

func tamper(t *testing.T, env string, mut func(*sseEnvelope)) string {
	t.Helper()
	var e sseEnvelope
	if err := json.Unmarshal([]byte(env), &e); err != nil {
		t.Fatal(err)
	}
	mut(&e)
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A corrupt/short IV must error gracefully, never panic in gcm.Open.
func TestDecrypt_RejectsBadIV(t *testing.T) {
	enc, ct, env := mustEncrypt(t)
	bad := tamper(t, env, func(e *sseEnvelope) {
		e.IV = base64.StdEncoding.EncodeToString(make([]byte, 8))
	})
	_, err := enc.decrypt(ct, bad)
	if err == nil || !strings.Contains(err.Error(), "iv is") {
		t.Fatalf("expected iv length error, got %v", err)
	}
}

func TestDecrypt_RejectsBadAlg(t *testing.T) {
	enc, ct, env := mustEncrypt(t)
	bad := tamper(t, env, func(e *sseEnvelope) { e.Alg = "AES-128-GCM" })
	_, err := enc.decrypt(ct, bad)
	if err == nil || !strings.Contains(err.Error(), "unsupported envelope alg") {
		t.Fatalf("expected alg rejection, got %v", err)
	}
}

func TestDecrypt_RejectsMalformedJSON(t *testing.T) {
	enc, ct, _ := mustEncrypt(t)
	_, err := enc.decrypt(ct, `{not json`)
	if err == nil || !strings.Contains(err.Error(), "malformed envelope") {
		t.Fatalf("expected malformed envelope error, got %v", err)
	}
}

// fixedProvider is a minimal external SecretProvider for testing the AES-256
// key-size guard.
type fixedProvider struct {
	id  string
	key []byte
}

func (p fixedProvider) Current() (string, []byte) { return p.id, p.key }
func (p fixedProvider) Resolve(id string) ([]byte, bool) {
	if id == p.id {
		return p.key, true
	}
	return nil, false
}

// An external provider returning a non-32-byte key is rejected, not silently
// downgraded to AES-128/192.
func TestExternalProvider_WrongKeySize(t *testing.T) {
	enc := newEnvelopeEncrypter(fixedProvider{id: "x", key: make([]byte, 16)})
	if _, _, err := enc.encrypt([]byte("data")); err == nil || !strings.Contains(err.Error(), "want 32") {
		t.Fatalf("expected 32-byte enforcement, got %v", err)
	}
	// The backend surfaces it on Put rather than writing a weak blob.
	s, err := NewLocal(LocalConfig{Root: t.TempDir(), Secrets: fixedProvider{id: "x", key: make([]byte, 16)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(context.Background(), "default/x.txt", bytes.NewReader([]byte("y")), 1, PutOptions{}); err == nil {
		t.Fatal("Put should fail with an undersized master key")
	}
}
