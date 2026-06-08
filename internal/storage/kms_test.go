package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockKMS is a tiny reversible "KMS": wrap = XOR each byte with 0xAA (and unwrap
// the same), proving the wrap/unwrap round-trip plumbing without real crypto.
func mockKMS(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	xor := func(b []byte) []byte {
		out := make([]byte, len(b))
		for i, c := range b {
			out[i] = c ^ 0xAA
		}
		return out
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in struct {
			KeyID      string `json:"key_id"`
			Plaintext  string `json:"plaintext"`
			Ciphertext string `json:"ciphertext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case strings.HasSuffix(r.URL.Path, "/wrap"):
			pt, _ := base64.StdEncoding.DecodeString(in.Plaintext)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"key_id":     in.KeyID + "#v1",
				"ciphertext": base64.StdEncoding.EncodeToString(xor(pt)),
			})
		case strings.HasSuffix(r.URL.Path, "/unwrap"):
			ctb, _ := base64.StdEncoding.DecodeString(in.Ciphertext)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"plaintext": base64.StdEncoding.EncodeToString(xor(ctb)),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// End-to-end: an object encrypted via a remote KMS wrapper round-trips, is stored
// as ciphertext, and its envelope records wrap:"kms" with the echoed key id.
func TestKMS_EncryptDecryptRoundTrip(t *testing.T) {
	ctx := context.Background()
	srv, calls := mockKMS(t)
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir, SSEKMSURL: srv.URL, SSEKMSKeyID: "alias/aero"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("guarded by a remote KMS")
	if _, err := s.Put(ctx, "default/k.txt", bytes.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	rc, _, err := s.Get(ctx, "default/k.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("kms roundtrip mismatch: %q", got)
	}
	if *calls < 2 {
		t.Fatalf("expected at least a wrap and an unwrap call, got %d", *calls)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "default", "k.txt"))
	if bytes.Contains(raw, []byte("guarded by a remote KMS")) {
		t.Fatal("object stored in plaintext despite KMS SSE")
	}
	meta, err := readMeta(filepath.Join(dir, "default", "k.txt") + localMetaSuffix)
	if err != nil {
		t.Fatal(err)
	}
	env, err := parseEnvelope(meta.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if env.Wrap != "kms" {
		t.Fatalf("envelope should record wrap=kms, got %q", env.Wrap)
	}
	if env.Kid != "alias/aero#v1" {
		t.Fatalf("envelope should record the KMS-echoed key id, got %q", env.Kid)
	}
}

// A KMS-wrapped object cannot be read by a local-provider backend, and a local
// object cannot be read by a KMS backend — each fails loudly rather than silently.
func TestKMS_CrossModeRejected(t *testing.T) {
	ctx := context.Background()
	srv, _ := mockKMS(t)
	dir := t.TempDir()

	// Write with KMS.
	ks, _ := NewLocal(LocalConfig{Root: dir, SSEKMSURL: srv.URL, SSEKMSKeyID: "k"})
	body := []byte("x")
	if _, err := ks.Put(ctx, "default/o", bytes.NewReader(body), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	// Try to read the KMS object with a local passphrase backend.
	ps, _ := NewLocal(LocalConfig{Root: dir, SSEKey: "local-pass"})
	if _, _, err := ps.Get(ctx, "default/o"); err == nil {
		t.Fatal("local backend must not decrypt a KMS-wrapped object")
	}

	// And the reverse: a local-wrapped object read by a KMS backend.
	dir2 := t.TempDir()
	ps2, _ := NewLocal(LocalConfig{Root: dir2, SSEKey: "local-pass"})
	if _, err := ps2.Put(ctx, "default/o", bytes.NewReader(body), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ks2, _ := NewLocal(LocalConfig{Root: dir2, SSEKMSURL: srv.URL, SSEKMSKeyID: "k"})
	if _, _, err := ks2.Get(ctx, "default/o"); err == nil {
		t.Fatal("KMS backend must not decrypt a locally-wrapped object")
	}
}

// A KMS error surfaces on Put rather than writing an unencrypted (or broken) blob.
func TestKMS_WrapErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "kms down")
	}))
	defer bad.Close()
	s, err := NewLocal(LocalConfig{Root: t.TempDir(), SSEKMSURL: bad.URL, SSEKMSKeyID: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "default/o", bytes.NewReader([]byte("y")), 1, PutOptions{}); err == nil {
		t.Fatal("Put should fail when the KMS wrap call errors")
	}
}

// A KMS that returns a wrong-sized unwrapped key is rejected (the data key must be
// exactly 32 bytes for AES-256), rather than producing a broken read.
func TestKMS_WrongSizeUnwrapRejected(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// First write a real KMS object with the proper mock.
	good, _ := mockKMS(t)
	s, _ := NewLocal(LocalConfig{Root: dir, SSEKMSURL: good.URL, SSEKMSKeyID: "k"})
	if _, err := s.Put(ctx, "default/o", bytes.NewReader([]byte("z")), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	// Now read it back via a KMS whose /unwrap returns a 16-byte key.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/unwrap") {
			_, _ = io.WriteString(w, `{"plaintext":"`+base64.StdEncoding.EncodeToString(make([]byte, 16))+`"}`)
		}
	}))
	defer bad.Close()
	s2, _ := NewLocal(LocalConfig{Root: dir, SSEKMSURL: bad.URL, SSEKMSKeyID: "k"})
	if _, _, err := s2.Get(ctx, "default/o"); err == nil {
		t.Fatal("a wrong-sized unwrapped key must be rejected")
	}
}

// Re-wrap is a no-op for KMS objects (the KMS owns its key rotation).
func TestKMS_RewrapIsNoOp(t *testing.T) {
	ctx := context.Background()
	srv, _ := mockKMS(t)
	dir := t.TempDir()
	s, _ := NewLocal(LocalConfig{Root: dir, SSEKMSURL: srv.URL, SSEKMSKeyID: "k"})
	if _, err := s.Put(ctx, "default/o", bytes.NewReader([]byte("z")), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if done, err := s.RewrapObject(ctx, "default/o"); err != nil || done {
		t.Fatalf("KMS object rewrap should be a no-op, got done=%v err=%v", done, err)
	}
}
