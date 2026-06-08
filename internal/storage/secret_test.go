package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeKeyfile(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keys.json")
	mustWrite(t, p, doc)
	return p
}

// The env provider stamps no key id, so its envelopes stay byte-compatible with
// the pre-rotation format and round-trip unchanged.
func TestEnvProvider_NoKidEnvelope(t *testing.T) {
	p, err := newEnvProvider("pw")
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := p.Current(); id != "" {
		t.Fatalf("env provider should stamp empty id, got %q", id)
	}
	enc := newEnvelopeEncrypter(p)
	ct, env, err := enc.encrypt([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(env, `"kid"`) {
		t.Fatalf("env-provider envelope must not carry a kid: %s", env)
	}
	got, err := enc.decrypt(ct, env)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}

func TestEnvProvider_RequiresPassphrase(t *testing.T) {
	if _, err := newEnvProvider(""); err == nil {
		t.Fatal("empty passphrase should error")
	}
}

func TestKeyfileProvider_Validation(t *testing.T) {
	cases := map[string]string{
		"bad json":          `{`,
		"no keys":           `{"primary":"v1","keys":{}}`,
		"no primary":        `{"keys":{"v1":"pw"}}`,
		"primary missing":   `{"primary":"v2","keys":{"v1":"pw"}}`,
		"reserved empty id": `{"primary":"v1","keys":{"v1":"pw","":"x"}}`,
		"empty passphrase":  `{"primary":"v1","keys":{"v1":""}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newKeyfileProvider(writeKeyfile(t, doc), ""); err == nil {
				t.Fatalf("expected error for %q", name)
			}
		})
	}
	if _, err := newKeyfileProvider(filepath.Join(t.TempDir(), "nope.json"), ""); err == nil {
		t.Fatal("missing file should error")
	}
}

// Rotation: write under v1, add v2 as primary (keeping v1), verify the old object
// still decrypts and new writes use v2; then retire v1 and confirm the old object
// fails with a clear, non-silent error.
func TestKeyfileProvider_Rotation(t *testing.T) {
	p1, err := newKeyfileProvider(writeKeyfile(t, `{"primary":"v1","keys":{"v1":"pw-one"}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	ct, env, err := newEnvelopeEncrypter(p1).encrypt([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env, `"kid":"v1"`) {
		t.Fatalf("expected kid v1 in envelope: %s", env)
	}

	// Rotate: v2 primary, v1 retained.
	p2, err := newKeyfileProvider(writeKeyfile(t, `{"primary":"v2","keys":{"v1":"pw-one","v2":"pw-two"}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	enc2 := newEnvelopeEncrypter(p2)
	got, err := enc2.decrypt(ct, env)
	if err != nil {
		t.Fatalf("post-rotation decrypt of v1 object: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("mismatch: %q", got)
	}
	_, env2, err := enc2.encrypt([]byte("payload2"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env2, `"kid":"v2"`) {
		t.Fatalf("expected kid v2: %s", env2)
	}

	// Retire v1: only v2 remains → old object is unreadable, with a clear error.
	p3, err := newKeyfileProvider(writeKeyfile(t, `{"primary":"v2","keys":{"v2":"pw-two"}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = newEnvelopeEncrypter(p3).decrypt(ct, env)
	if err == nil || !strings.Contains(err.Error(), "unknown key version") {
		t.Fatalf("expected unknown key version error, got %v", err)
	}
}

// A keyfile provider given the former env passphrase decrypts pre-versioning,
// no-kid objects; without it, those objects error rather than silently fail.
func TestKeyfileProvider_LegacyNoKid(t *testing.T) {
	const legacy = "old-env-key"
	envP, _ := newEnvProvider(legacy)
	ct, env, err := newEnvelopeEncrypter(envP).encrypt([]byte("legacy-data"))
	if err != nil {
		t.Fatal(err)
	}

	withLegacy, err := newKeyfileProvider(writeKeyfile(t, `{"primary":"v1","keys":{"v1":"new-key"}}`), legacy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := newEnvelopeEncrypter(withLegacy).decrypt(ct, env)
	if err != nil {
		t.Fatalf("legacy decrypt: %v", err)
	}
	if string(got) != "legacy-data" {
		t.Fatalf("mismatch: %q", got)
	}

	noLegacy, _ := newKeyfileProvider(writeKeyfile(t, `{"primary":"v1","keys":{"v1":"new-key"}}`), "")
	if _, err := newEnvelopeEncrypter(noLegacy).decrypt(ct, env); err == nil {
		t.Fatal("expected error decrypting no-kid object without legacy key")
	}
}

// End-to-end through the local backend: write under a keyfile, rotate it in place,
// reopen the backend, and confirm old objects read and new objects write/read.
func TestLocalSSE_KeyfileRotationE2E(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	kf := filepath.Join(t.TempDir(), "keys.json")
	mustWrite(t, kf, `{"primary":"2025","keys":{"2025":"first"}}`)

	s1, err := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("rotate me")
	if _, err := s1.Put(ctx, "default/a.txt", bytes.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatal(err)
	}

	// Rotate the keyfile in place (add 2026, keep 2025), reopen the backend.
	mustWrite(t, kf, `{"primary":"2026","keys":{"2025":"first","2026":"second"}}`)
	s2, err := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := s2.Get(ctx, "default/a.txt")
	if err != nil {
		t.Fatalf("read pre-rotation object: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("mismatch: %q", got)
	}

	body2 := []byte("new key era")
	if _, err := s2.Put(ctx, "default/b.txt", bytes.NewReader(body2), int64(len(body2)), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	rc2, _, err := s2.Get(ctx, "default/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := io.ReadAll(rc2)
	_ = rc2.Close()
	if !bytes.Equal(got2, body2) {
		t.Fatalf("mismatch: %q", got2)
	}
}

func TestKeyfileProvider_InvalidKeyID(t *testing.T) {
	if _, err := newKeyfileProvider(writeKeyfile(t, `{"primary":"a b","keys":{"a b":"pw"}}`), ""); err == nil {
		t.Fatal("space in key id should be rejected")
	}
	if _, err := newKeyfileProvider(writeKeyfile(t, `{"primary":"a\"b","keys":{"a\"b":"pw"}}`), ""); err == nil {
		t.Fatal("quote in key id should be rejected")
	}
}

// Multipart uploads must be encrypted on completion when SSE is enabled — read
// back as plaintext through the API, but stored as ciphertext on disk.
func TestLocalSSE_Multipart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir, SSEKey: "mp-key"})
	if err != nil {
		t.Fatal(err)
	}
	init, err := s.InitMultipart(ctx, "default/big.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := []byte("first-part-"), []byte("second-part")
	mp1, err := s.UploadPart(ctx, "default/big.bin", init.UploadID, 1, bytes.NewReader(p1), int64(len(p1)))
	if err != nil {
		t.Fatal(err)
	}
	mp2, err := s.UploadPart(ctx, "default/big.bin", init.UploadID, 2, bytes.NewReader(p2), int64(len(p2)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteMultipart(ctx, "default/big.bin", init.UploadID, []MultipartPart{mp1, mp2}); err != nil {
		t.Fatal(err)
	}

	rc, _, err := s.Get(ctx, "default/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	want := append(append([]byte{}, p1...), p2...)
	if !bytes.Equal(got, want) {
		t.Fatalf("multipart SSE roundtrip mismatch: %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "default", "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("first-part-")) {
		t.Fatal("multipart object stored in plaintext despite SSE")
	}
}

// Objects written before SSE was enabled (no envelope) must still read back as
// plaintext when the backend later has SSE on.
func TestPlaintextObjectReadableUnderSSE(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	plain, err := NewLocal(LocalConfig{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("not encrypted")
	if _, err := plain.Put(ctx, "default/p.txt", bytes.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	withSSE, err := NewLocal(LocalConfig{Root: dir, SSEKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := withSSE.Get(ctx, "default/p.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("plaintext passthrough under SSE broke: %q", got)
	}
}

// The HTTP secret-store provider loads a key ring over HTTP (sending the bearer
// token), supports the same versioned semantics, and end-to-ends through NewLocal.
func TestHTTPProvider_LoadsAndEncrypts(t *testing.T) {
	ctx := context.Background()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"primary":"v2","keys":{"v1":"old-pass","v2":"new-pass"}}`)
	}))
	defer srv.Close()

	p, err := newHTTPProvider(srv.URL, "s3cr3t", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Fatalf("bearer token not sent, got %q", gotAuth)
	}
	if id, _ := p.Current(); id != "v2" {
		t.Fatalf("current should be v2, got %q", id)
	}
	if _, ok := p.Resolve("v1"); !ok {
		t.Fatal("v1 should resolve")
	}

	// End-to-end through the backend via STORAGE_LOCAL_SSE_KEY_URL.
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir, SSEKeyURL: srv.URL})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	body := []byte("via http secret store")
	if _, err := s.Put(ctx, "default/h.txt", bytes.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	rc, _, err := s.Get(ctx, "default/h.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("http-provider roundtrip mismatch: %q", got)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "default", "h.txt"))
	if bytes.Contains(raw, []byte("via http secret store")) {
		t.Fatal("object stored in plaintext despite SSE")
	}
}

func TestHTTPProvider_Errors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "denied")
	}))
	defer bad.Close()
	if _, err := newHTTPProvider(bad.URL, "", ""); err == nil {
		t.Fatal("403 should error")
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"primary":"v1","keys":{}}`)
	}))
	defer malformed.Close()
	if _, err := newHTTPProvider(malformed.URL, "", ""); err == nil {
		t.Fatal("empty key ring should error")
	}

	if _, err := newHTTPProvider("http://127.0.0.1:0/keys", "", ""); err == nil {
		t.Fatal("unreachable url should error")
	}
}
