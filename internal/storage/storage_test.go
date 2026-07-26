package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLocalBackend(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if got := s.Backend(); got != "local" {
		t.Errorf("Backend() = %q, want %q", got, "local")
	}
}

func TestTimeoutConfigDefaults(t *testing.T) {
	tc := DefaultTimeoutConfig()
	if tc.ConnectTimeout <= 0 || tc.ReadTimeout <= 0 || tc.WriteTimeout <= 0 {
		t.Fatal("default timeouts should all be positive")
	}
}

func TestNewHTTPClientZero(t *testing.T) {
	hc := NewHTTPClient(TimeoutConfig{})
	if hc != http.DefaultClient {
		t.Error("zero TimeoutConfig should return http.DefaultClient")
	}
}

func TestNewHTTPClientCustom(t *testing.T) {
	tc := TimeoutConfig{ConnectTimeout: 2 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second}
	hc := NewHTTPClient(tc)
	if hc == http.DefaultClient {
		t.Error("custom TimeoutConfig should return a new client")
	}
	if hc.Timeout <= 0 {
		t.Error("client should have a non-zero timeout")
	}
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport should be *http.Transport")
	}
	if tr.ResponseHeaderTimeout != 10*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 10s", tr.ResponseHeaderTimeout)
	}
}

func TestPresignPutGet(t *testing.T) {
	s, err := NewLocal(LocalConfig{
		Root:      t.TempDir(),
		SignKey:   "test-hmac-key-32-bytes-long!!",
		PublicURL: "http://test/files",
	})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()

	t.Run("put url", func(t *testing.T) {
		u, err := s.PresignPut(ctx, "test/key.txt", 5*time.Minute)
		if err != nil {
			t.Fatalf("PresignPut: %v", err)
		}
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}
		if parsed.Query().Get("sig") == "" {
			t.Error("presigned URL missing sig parameter")
		}
		if parsed.Query().Get("method") != "PUT" {
			t.Errorf("method = %q, want PUT", parsed.Query().Get("method"))
		}
		exp := parsed.Query().Get("expires")
		if exp == "" {
			t.Error("presigned URL missing expires")
		}
	})

	t.Run("get url", func(t *testing.T) {
		u, err := s.PresignGet(ctx, "test/key.txt", 5*time.Minute)
		if err != nil {
			t.Fatalf("PresignGet: %v", err)
		}
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}
		if parsed.Query().Get("sig") == "" {
			t.Error("presigned URL missing sig parameter")
		}
		if parsed.Query().Get("method") != "GET" {
			t.Errorf("method = %q, want GET", parsed.Query().Get("method"))
		}
	})

	t.Run("disabled without sign key", func(t *testing.T) {
		disabled, err := NewLocal(LocalConfig{Root: t.TempDir(), PublicURL: "http://test/files"})
		if err != nil {
			t.Fatalf("new local: %v", err)
		}
		if _, err := disabled.PresignPut(ctx, "test/key.txt", time.Minute); err == nil {
			t.Error("expected error when SignKey is empty")
		}
		if _, err := disabled.PresignGet(ctx, "test/key.txt", time.Minute); err == nil {
			t.Error("expected error when SignKey is empty")
		}
	})

	t.Run("disabled without public URL", func(t *testing.T) {
		disabled, err := NewLocal(LocalConfig{Root: t.TempDir(), SignKey: "key"})
		if err != nil {
			t.Fatalf("new local: %v", err)
		}
		if _, err := disabled.PresignPut(ctx, "test/key.txt", time.Minute); err == nil {
			t.Error("expected error when PublicURL is empty")
		}
	})

	t.Run("rejects invalid key", func(t *testing.T) {
		if _, err := s.PresignPut(ctx, "/absolute/key", time.Minute); err == nil {
			t.Error("expected error for absolute key")
		}
		if _, err := s.PresignGet(ctx, "../escape", time.Minute); err == nil {
			t.Error("expected error for traversal key")
		}
	})
}

func TestVerifyLocalSig(t *testing.T) {
	signKey := "my-signing-key"

	t.Run("valid signature", func(t *testing.T) {
		exp := time.Now().Add(time.Hour).Unix()
		sig := signLocal(signKey, "GET", "my/object.txt", exp)
		if !VerifyLocalSig(signKey, "GET", "my/object.txt", exp, sig) {
			t.Error("VerifyLocalSig returned false for valid signature")
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		exp := time.Now().Add(time.Hour).Unix()
		sig := signLocal(signKey, "GET", "my/object.txt", exp)
		if VerifyLocalSig(signKey, "PUT", "my/object.txt", exp, sig) {
			t.Error("VerifyLocalSig should reject wrong method")
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		exp := time.Now().Add(time.Hour).Unix()
		sig := signLocal(signKey, "GET", "my/object.txt", exp)
		if VerifyLocalSig("wrong-key", "GET", "my/object.txt", exp, sig) {
			t.Error("VerifyLocalSig should reject wrong key")
		}
	})

	t.Run("wrong object key", func(t *testing.T) {
		exp := time.Now().Add(time.Hour).Unix()
		sig := signLocal(signKey, "GET", "my/object.txt", exp)
		if VerifyLocalSig(signKey, "GET", "other/object.txt", exp, sig) {
			t.Error("VerifyLocalSig should reject wrong object key")
		}
	})

	t.Run("expired", func(t *testing.T) {
		exp := time.Now().Add(-time.Hour).Unix()
		sig := signLocal(signKey, "GET", "my/object.txt", exp)
		if VerifyLocalSig(signKey, "GET", "my/object.txt", exp, sig) {
			t.Error("VerifyLocalSig should reject expired signature")
		}
	})

	t.Run("empty sign key", func(t *testing.T) {
		if VerifyLocalSig("", "GET", "key", 0, "sig") {
			t.Error("VerifyLocalSig should return false with empty sign key")
		}
	})
}

func TestAbortMultipart(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()

	t.Run("abort cleans up temp dir", func(t *testing.T) {
		init, err := s.InitMultipart(ctx, "test/mp.txt", PutOptions{})
		if err != nil {
			t.Fatalf("InitMultipart: %v", err)
		}
		dir := filepath.Join(s.cfg.Root, ".multipart", init.UploadID)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Fatal("multipart temp dir should exist after init")
		}
		part, err := s.UploadPart(ctx, "test/mp.txt", init.UploadID, 1, strings.NewReader("part data"), 9)
		if err != nil {
			t.Fatalf("UploadPart: %v", err)
		}
		partPath := filepath.Join(dir, "part-00001")
		if _, err := os.Stat(partPath); os.IsNotExist(err) {
			t.Fatal("part file should exist after upload")
		}
		if err := s.AbortMultipart(ctx, "test/mp.txt", init.UploadID); err != nil {
			t.Fatalf("AbortMultipart: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("multipart temp dir should be removed after abort")
		}
		if _, err := os.Stat(partPath); !os.IsNotExist(err) {
			t.Fatal("part file should be removed after abort")
		}
		_ = part
	})

	t.Run("abort unknown upload is not an error", func(t *testing.T) {
		if err := s.AbortMultipart(ctx, "nonexistent", "bogus-upload-id"); err != nil {
			t.Errorf("aborting unknown upload should not error, got %v", err)
		}
	})

	t.Run("double abort is not an error", func(t *testing.T) {
		init, err := s.InitMultipart(ctx, "test/double-abort.txt", PutOptions{})
		if err != nil {
			t.Fatalf("InitMultipart: %v", err)
		}
		if err := s.AbortMultipart(ctx, "test/double-abort.txt", init.UploadID); err != nil {
			t.Fatalf("first AbortMultipart: %v", err)
		}
		if err := s.AbortMultipart(ctx, "test/double-abort.txt", init.UploadID); err != nil {
			t.Errorf("second AbortMultipart should not error, got %v", err)
		}
	})
}

func TestEncryptReader(t *testing.T) {
	s, err := NewLocal(LocalConfig{
		Root:   t.TempDir(),
		SSEKey: "test-sse-key-32bytes!!",
	})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if s.enc == nil {
		t.Fatal("encrypter should be non-nil with SSEKey set")
	}

	plaintext := []byte("hello, encrypted world!")
	er, env, err := encryptReader(bytes.NewReader(plaintext), s.enc)
	if err != nil {
		t.Fatalf("encryptReader: %v", err)
	}
	if env == "" {
		t.Fatal("encryptReader returned empty envelope")
	}

	ct, err := io.ReadAll(er)
	if err != nil {
		t.Fatalf("read encrypted data: %v", err)
	}
	if bytes.Equal(ct, plaintext) {
		t.Fatal("ciphertext should not equal plaintext")
	}
	if len(ct) == 0 {
		t.Fatal("ciphertext should not be empty")
	}

	dr, err := decryptReader(bytes.NewReader(ct), env, s.enc)
	if err != nil {
		t.Fatalf("decryptReader: %v", err)
	}
	got, err := io.ReadAll(dr)
	if err != nil {
		t.Fatalf("read decrypted data: %v", err)
	}
	_ = dr.Close()
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted data mismatch: got %q, want %q", string(got), string(plaintext))
	}
}

func TestEncryptReader_RoundtripViaPutGet(t *testing.T) {
	s, err := NewLocal(LocalConfig{
		Root:   t.TempDir(),
		SSEKey: "another-test-key-32byte!!",
	})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	body := []byte("encrypted roundtrip payload")

	if _, err := s.Put(ctx, "default/encrypted.txt", bytes.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, info, err := s.Get(ctx, "default/encrypted.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("roundtrip mismatch: got %q, want %q", string(got), string(body))
	}
	if info.Size != int64(len(body)) {
		t.Errorf("info.Size = %d, want %d", info.Size, len(body))
	}
}

func TestPresignAndVerifyEndToEnd(t *testing.T) {
	s, err := NewLocal(LocalConfig{
		Root:      t.TempDir(),
		SignKey:   "e2e-test-signing-key",
		PublicURL: "http://localhost:8080/files",
	})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()

	u, err := s.PresignGet(ctx, "e2e/object.bin", 10*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := parsed.Query()

	exp, _ := strconv.ParseInt(q.Get("expires"), 10, 64)
	sig := q.Get("sig")
	method := q.Get("method")

	key := strings.TrimPrefix(parsed.Path, "/files/")
	keyUnescaped, err := url.PathUnescape(key)
	if err != nil {
		t.Fatalf("unescape key: %v", err)
	}

	if !VerifyLocalSig("e2e-test-signing-key", method, keyUnescaped, exp, sig) {
		t.Error("VerifyLocalSig rejected a valid presigned URL")
	}
	if VerifyLocalSig("wrong-key", method, keyUnescaped, exp, sig) {
		t.Error("VerifyLocalSig accepted with wrong key")
	}
}

func TestList_HasMore(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("items/item-%d.txt", i)
		if _, err := s.Put(ctx, key, strings.NewReader("data"), 4, PutOptions{}); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}

	t.Run("limit 1 returns HasMore", func(t *testing.T) {
		res, err := s.List(ctx, "", "", 1)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if !res.HasMore {
			t.Error("List with limit=1 should have HasMore=true")
		}
		if len(res.Objects) != 1 {
			t.Errorf("got %d objects, want 1", len(res.Objects))
		}
		if res.NextMarker == "" {
			t.Error("NextMarker should be non-empty")
		}
	})

	t.Run("limit 3 returns all without HasMore", func(t *testing.T) {
		res, err := s.List(ctx, "", "", 3)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if res.HasMore {
			t.Error("List with limit=3 should have HasMore=false")
		}
		if len(res.Objects) != 3 {
			t.Errorf("got %d objects, want 3", len(res.Objects))
		}
	})

	t.Run("pagination with marker", func(t *testing.T) {
		res1, err := s.List(ctx, "", "", 2)
		if err != nil {
			t.Fatalf("List page 1: %v", err)
		}
		if !res1.HasMore {
			t.Fatal("expected HasMore for page 1")
		}
		res2, err := s.List(ctx, "", res1.NextMarker, 2)
		if err != nil {
			t.Fatalf("List page 2: %v", err)
		}
		if res2.HasMore {
			t.Error("page 2 should not have HasMore")
		}
		if len(res2.Objects) != 1 {
			t.Errorf("page 2 got %d objects, want 1", len(res2.Objects))
		}
	})
}

func TestStatNoSidecar(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()

	key := "bare/object.txt"
	_, err = s.Put(ctx, key, strings.NewReader("hello world"), 11, PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	path, err := s.objectPath(key)
	if err != nil {
		t.Fatalf("objectPath: %v", err)
	}
	if err := os.Remove(s.metaPath(path)); err != nil {
		t.Fatalf("remove meta: %v", err)
	}

	info, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat after removing sidecar: %v", err)
	}
	if info.Size != 11 {
		t.Errorf("info.Size = %d, want 11", info.Size)
	}
}

func TestGetNotFound(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if _, _, err := s.Get(context.Background(), "nonexistent"); err != ErrNotFound {
		t.Errorf("Get nonexistent: got %v, want ErrNotFound", err)
	}
}

func TestStatNotFound(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if _, err := s.Stat(context.Background(), "nonexistent"); err != ErrNotFound {
		t.Errorf("Stat nonexistent: got %v, want ErrNotFound", err)
	}
}

func TestUploadPartUnknownUpload(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	_, err = s.UploadPart(context.Background(), "key", "nonexistent-upload-id", 1, strings.NewReader("data"), 4)
	if err == nil {
		t.Fatal("UploadPart should fail with unknown upload ID")
	}
}

func TestCompleteMultipartUnknownUpload(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if _, err := s.CompleteMultipart(context.Background(), "key", "nonexistent-upload-id", nil); err == nil {
		t.Fatal("CompleteMultipart should fail with unknown upload ID")
	}
}

func TestDeleteWithError(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()

	_, err = s.Put(ctx, "test/locked.txt", strings.NewReader("data"), 4, PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	sub := filepath.Join(dir, "test")
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Skipf("chmod failed: %v", err)
	}
	defer os.Chmod(sub, 0o755)

	if err := s.Delete(ctx, "test/locked.txt"); err == nil {
		t.Error("Delete should fail when object dir is read-only")
	}

	os.Chmod(sub, 0o755)
	if err := s.Delete(ctx, "test/locked.txt"); err != nil {
		t.Errorf("Delete after restoring permissions: %v", err)
	}
}

func TestBytesReader(t *testing.T) {
	t.Run("non-empty", func(t *testing.T) {
		r := bytesReader([]byte("hello"))
		buf := make([]byte, 10)
		n, err := r.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if n != 5 {
			t.Errorf("read %d bytes, want 5", n)
		}
		if string(buf[:n]) != "hello" {
			t.Errorf("got %q, want %q", string(buf[:n]), "hello")
		}
	})

	t.Run("empty", func(t *testing.T) {
		r := bytesReader([]byte{})
		buf := make([]byte, 10)
		_, err := r.Read(buf)
		if err != io.EOF {
			t.Errorf("Read on empty: got %v, want EOF", err)
		}
	})

	t.Run("partial read", func(t *testing.T) {
		r := bytesReader([]byte("abcdef"))
		buf := make([]byte, 2)
		n1, _ := r.Read(buf)
		if n1 != 2 || string(buf[:n1]) != "ab" {
			t.Fatalf("first read: %d %q", n1, string(buf[:n1]))
		}
		n2, _ := r.Read(buf)
		if n2 != 2 || string(buf[:n2]) != "cd" {
			t.Fatalf("second read: %d %q", n2, string(buf[:n2]))
		}
		n3, _ := r.Read(buf)
		if n3 != 2 || string(buf[:n3]) != "ef" {
			t.Fatalf("third read: %d %q", n3, string(buf[:n3]))
		}
		_, err := r.Read(buf)
		if err != io.EOF {
			t.Fatalf("fourth read should be EOF, got %v", err)
		}
	})
}

func TestPlaintextSize(t *testing.T) {
	tests := []struct {
		written   int64
		encrypted bool
		want      int64
	}{
		{100, false, 100},
		{100, true, 84},
		{10, true, 0},
		{0, true, 0},
	}
	for _, tt := range tests {
		got := plaintextSize(tt.written, tt.encrypted)
		if got != tt.want {
			t.Errorf("plaintextSize(%d, %v) = %d, want %d", tt.written, tt.encrypted, got, tt.want)
		}
	}
}

func TestLocalNewLocalErrors(t *testing.T) {
	if _, err := NewLocal(LocalConfig{Root: ""}); err == nil {
		t.Error("NewLocal with empty root should error")
	}
}

func TestListWithPrefixAndMarker(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	keys := []string{"alpha/one", "alpha/two", "beta/one", "gamma/one", "alpha/three"}
	for _, k := range keys {
		if _, err := s.Put(ctx, k, strings.NewReader("d"), 1, PutOptions{}); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}

	t.Run("prefix filter", func(t *testing.T) {
		res, err := s.List(ctx, "alpha/", "", 100)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(res.Objects) != 3 {
			t.Errorf("got %d objects for prefix alpha/, want 3", len(res.Objects))
		}
	})

	t.Run("prefix with marker", func(t *testing.T) {
		res, err := s.List(ctx, "alpha/", "alpha/one", 100)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(res.Objects) != 2 {
			t.Errorf("got %d objects after marker, want 2", len(res.Objects))
		}
	})

	t.Run("non-matching prefix", func(t *testing.T) {
		res, err := s.List(ctx, "zzz/", "", 100)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(res.Objects) != 0 {
			t.Errorf("got %d objects for non-matching prefix, want 0", len(res.Objects))
		}
	})

	t.Run("empty prefix returns all", func(t *testing.T) {
		res, err := s.List(ctx, "", "", 100)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(res.Objects) != 5 {
			t.Errorf("got %d objects for empty prefix, want 5", len(res.Objects))
		}
	})

	t.Run("default limit when zero", func(t *testing.T) {
		res, err := s.List(ctx, "", "", 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(res.Objects) != 5 {
			t.Errorf("got %d objects with default limit, want 5", len(res.Objects))
		}
	})
}

func TestDeleteNonExistent(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if err := s.Delete(context.Background(), "nonexistent/key.txt"); err != nil {
		t.Errorf("Delete nonexistent should not error, got %v", err)
	}
}

func TestDeleteInvalidKey(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if err := s.Delete(context.Background(), "/absolute"); err == nil {
		t.Error("Delete with absolute key should error")
	}
}

func TestGetInvalidKey(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if _, _, err := s.Get(context.Background(), "../escape"); err == nil {
		t.Error("Get with traversal key should error")
	}
}

func TestStatInvalidKey(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if _, err := s.Stat(context.Background(), ""); err == nil {
		t.Error("Stat with empty key should error")
	}
}

func TestMultipartWithSSE(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir, SSEKey: "mp-sse-key-32bytes-long!!"})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()

	init, err := s.InitMultipart(ctx, "test/sse-mp.bin", PutOptions{})
	if err != nil {
		t.Fatalf("InitMultipart: %v", err)
	}
	part1, err := s.UploadPart(ctx, "test/sse-mp.bin", init.UploadID, 1, strings.NewReader("part one data"), 13)
	if err != nil {
		t.Fatalf("UploadPart 1: %v", err)
	}
	part2, err := s.UploadPart(ctx, "test/sse-mp.bin", init.UploadID, 2, strings.NewReader("part two data"), 13)
	if err != nil {
		t.Fatalf("UploadPart 2: %v", err)
	}
	oi, err := s.CompleteMultipart(ctx, "test/sse-mp.bin", init.UploadID, []MultipartPart{part1, part2})
	if err != nil {
		t.Fatalf("CompleteMultipart: %v", err)
	}
	rc, _, err := s.Get(ctx, "test/sse-mp.bin")
	if err != nil {
		t.Fatalf("Get after multipart SSE: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	want := "part one datapart two data"
	if string(got) != want {
		t.Fatalf("multipart SSE content mismatch: got %q, want %q", string(got), want)
	}
	if oi.Size != 26 {
		t.Errorf("oi.Size = %d, want 26", oi.Size)
	}
}

func TestFactoryDefault(t *testing.T) {
	if _, err := NewFromConfig(context.Background(), FactoryConfig{Kind: "unknown"}); err == nil {
		t.Error("NewFromConfig with unknown kind should error")
	}
	if _, err := NewFromConfig(context.Background(), FactoryConfig{Kind: BackendLocal, Local: LocalConfig{Root: t.TempDir()}}); err != nil {
		t.Fatalf("NewFromConfig local: %v", err)
	}
}

func TestFactoryS3Error(t *testing.T) {
	_, err := NewFromConfig(context.Background(), FactoryConfig{Kind: BackendS3})
	if err == nil {
		t.Error("NewFromConfig S3 should error with empty config")
	}
}

func TestObjectPathTraversal(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	_, err = s.objectPath("foo/../../etc/passwd")
	if err == nil {
		t.Error("objectPath should reject traversal via relative paths")
	}
}

func TestResolveOldKeyUnknown(t *testing.T) {
	p, err := newEnvProvider("test-pw-has-enough-chars-here!")
	if err != nil {
		t.Fatalf("newEnvProvider: %v", err)
	}
	enc := newEnvelopeEncrypter(p)

	_, err = enc.resolveOldKey("some-unknown-kid")
	if err == nil || !strings.Contains(err.Error(), "unknown key version") {
		t.Fatalf("expected unknown key version error, got %v", err)
	}
}

func TestWriteMetaCreateTempError(t *testing.T) {
	err := writeMeta("/nonexistent-parent-dir/meta.json", localMeta{Key: "test"})
	if err == nil {
		t.Error("expected error for non-existent parent directory")
	}
}

func TestRewrapStaleNonRewrapper(t *testing.T) {
	ctx := context.Background()
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	// store that only implements Storage, not Rewrapper
	nr := &nonRewrapStore{inner: s}
	rep, err := RewrapStale(ctx, nr)
	if err != nil {
		t.Fatalf("RewrapStale: %v", err)
	}
	if rep.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0", rep.Scanned)
	}
}

type nonRewrapStore struct {
	inner Storage
}

func (n *nonRewrapStore) Backend() string { return n.inner.Backend() }
func (n *nonRewrapStore) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
	return n.inner.Put(ctx, key, r, size, opts)
}
func (n *nonRewrapStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return n.inner.Get(ctx, key)
}
func (n *nonRewrapStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	return n.inner.Stat(ctx, key)
}
func (n *nonRewrapStore) CanCopy() bool { return n.inner.CanCopy() }

func (n *nonRewrapStore) Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error) {
	return n.inner.Copy(ctx, srcKey, dstKey, opts)
}

func (n *nonRewrapStore) Delete(ctx context.Context, key string) error {
	return n.inner.Delete(ctx, key)
}
func (n *nonRewrapStore) List(ctx context.Context, prefix, marker string, limit int) (ListResult, error) {
	return n.inner.List(ctx, prefix, marker, limit)
}
func (n *nonRewrapStore) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return n.inner.PresignGet(ctx, key, expiry)
}
func (n *nonRewrapStore) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return n.inner.PresignPut(ctx, key, expiry)
}
func (n *nonRewrapStore) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
	return n.inner.InitMultipart(ctx, key, opts)
}
func (n *nonRewrapStore) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
	return n.inner.UploadPart(ctx, key, uploadID, partNumber, r, size)
}
func (n *nonRewrapStore) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
	return n.inner.CompleteMultipart(ctx, key, uploadID, parts)
}
func (n *nonRewrapStore) AbortMultipart(ctx context.Context, key, uploadID string) error {
	return n.inner.AbortMultipart(ctx, key, uploadID)
}

func (n *nonRewrapStore) CleanupParts(ctx context.Context, key, uploadID string) error {
	return n.inner.CleanupParts(ctx, key, uploadID)
}

func (n *nonRewrapStore) UploadPartCopy(ctx context.Context, dstKey, uploadID string, partNumber int32, srcKey string, srcOffset, length int64) (MultipartPart, error) {
	return n.inner.UploadPartCopy(ctx, dstKey, uploadID, partNumber, srcKey, srcOffset, length)
}

func TestNewLocalWithKMSURL(t *testing.T) {
	s, err := NewLocal(LocalConfig{
		Root:        t.TempDir(),
		SSEKMSURL:   "http://kms.example.com",
		SSEKMSKeyID: "key-123",
	})
	if err != nil {
		t.Fatalf("new local with KMS: %v", err)
	}
	if s.enc == nil {
		t.Fatal("encrypter should be non-nil with SSEKMSURL set")
	}
}

func TestPlaintextObjectNotFoundOnGet(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	// Put an object without SSE, then remove sidecar
	_, err = s.Put(ctx, "test/plain.txt", strings.NewReader("hello"), 5, PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	path, _ := s.objectPath("test/plain.txt")
	os.Remove(s.metaPath(path))
	os.Remove(path)

	_, _, err = s.Get(ctx, "test/plain.txt")
	if err != ErrNotFound {
		t.Errorf("Get after removing everything: got %v, want ErrNotFound", err)
	}
}

func TestRewrapObjectWithSSEDisabled(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	done, err := s.RewrapObject(context.Background(), "any/key")
	if err != nil {
		t.Fatalf("RewrapObject with SSE disabled: %v", err)
	}
	if done {
		t.Error("RewrapObject should return false when SSE is disabled")
	}
}

func TestRewrapObjectNotFound(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir(), SSEKey: "test-sse-key-rewrap-32byte"})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	_, err = s.RewrapObject(context.Background(), "nonexistent")
	if err != ErrNotFound {
		t.Errorf("RewrapObject nonexistent: got %v, want ErrNotFound", err)
	}
}

func TestListWithMetaFileSkips(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	// Put an object
	_, err = s.Put(ctx, "visible/file.txt", strings.NewReader("data"), 4, PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Create a .meta.json file without a corresponding object blob - should be skipped
	dir := filepath.Join(s.cfg.Root, "visible")
	f, err := os.CreateTemp(dir, "*.meta.json")
	if err != nil {
		t.Fatalf("CreateTemp meta: %v", err)
	}
	f.Close()

	res, err := s.List(ctx, "", "", 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Objects) != 1 {
		t.Errorf("got %d objects, want 1 (only the real file)", len(res.Objects))
	}
}

type testKMS struct{}

func (k *testKMS) WrapKey(dataKey []byte) ([]byte, string, error) {
	wrapped := make([]byte, len(dataKey))
	for i, b := range dataKey {
		wrapped[i] = b ^ 0xAA
	}
	return wrapped, "test-kid", nil
}

func (k *testKMS) UnwrapKey(wrapped []byte, keyID string) ([]byte, error) {
	dataKey := make([]byte, len(wrapped))
	for i, b := range wrapped {
		dataKey[i] = b ^ 0xAA
	}
	return dataKey, nil
}

func TestReadMetaBadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.meta.json")
	if err := os.WriteFile(path, []byte("{not json}"), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if _, err := readMeta(path); err == nil {
		t.Error("readMeta should fail with bad JSON")
	}
}

func TestNewLocalWithSSEKeyfile(t *testing.T) {
	dir := t.TempDir()
	keyfile := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(keyfile, []byte(`{"primary":"v1","keys":{"v1":"test-master-key-32bytes-long!!"}}`), 0o644); err != nil {
		t.Fatalf("write keyfile: %v", err)
	}
	s, err := NewLocal(LocalConfig{
		Root:       t.TempDir(),
		SSEKeyfile: keyfile,
	})
	if err != nil {
		t.Fatalf("new local with keyfile: %v", err)
	}
	if s.enc == nil {
		t.Fatal("encrypter should be non-nil with SSEKeyfile set")
	}
	// Roundtrip a put/get through SSE using the keyfile provider
	ctx := context.Background()
	body := []byte("keyfile encrypted payload")
	if _, err := s.Put(ctx, "kf/test.txt", bytes.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatalf("Put with keyfile: %v", err)
	}
	rc, _, err := s.Get(ctx, "kf/test.txt")
	if err != nil {
		t.Fatalf("Get with keyfile: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("keyfile roundtrip mismatch: got %q, want %q", string(got), string(body))
	}
}

func TestStatWithBadMetaJSON(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	// Create object with a valid meta first
	if _, err := s.Put(ctx, "test/obj.txt", strings.NewReader("data"), 4, PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Overwrite meta with bad JSON
	path, _ := s.objectPath("test/obj.txt")
	if err := os.WriteFile(s.metaPath(path), []byte("{bad json}"), 0o644); err != nil {
		t.Fatalf("write bad meta: %v", err)
	}
	if _, err := s.Stat(ctx, "test/obj.txt"); err == nil {
		t.Error("Stat should fail with bad meta JSON")
	}
}

func TestGetWithPermissionDenied(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	if _, err := s.Put(ctx, "test/secure.txt", strings.NewReader("data"), 4, PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	path, _ := s.objectPath("test/secure.txt")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	defer os.Chmod(path, 0o644)

	_, _, err = s.Get(ctx, "test/secure.txt")
	if err == nil {
		t.Error("Get should fail with permission denied")
	}
}

func TestMultipartETagBadHex(t *testing.T) {
	_, err := multipartETag([]MultipartPart{{PartNumber: 1, ETag: "not-hex"}})
	if err == nil {
		t.Error("multipartETag should reject non-hex ETag")
	}
}

func TestCompleteMultipartInvalidKey(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	init, err := s.InitMultipart(context.Background(), "test/mp.txt", PutOptions{})
	if err != nil {
		t.Fatalf("InitMultipart: %v", err)
	}
	_, err = s.CompleteMultipart(context.Background(), "test/mp.txt", init.UploadID, []MultipartPart{{PartNumber: 1, ETag: "d41d8cd98f00b204e9800998ecf8427e"}})
	if err == nil {
		t.Error("CompleteMultipart should fail with a missing part file")
	}
}

func TestInitMultipartInvalidKey(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	if _, err := s.InitMultipart(context.Background(), "/absolute/key", PutOptions{}); err == nil {
		t.Error("InitMultipart with absolute key should error")
	}
}

func TestUploadPartCreateError(t *testing.T) {
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	// Init with a path that has no parent dir so part file create fails
	init, err := s.InitMultipart(context.Background(), "test/mp.txt", PutOptions{})
	if err != nil {
		t.Fatalf("InitMultipart: %v", err)
	}
	// Remove the upload tmp dir so UploadPart fails when trying to create a part
	dir := filepath.Join(s.cfg.Root, ".multipart", init.UploadID)
	os.RemoveAll(dir)

	_, err = s.UploadPart(context.Background(), "test/mp.txt", init.UploadID, 1, strings.NewReader("data"), 4)
	if err == nil {
		t.Error("UploadPart should fail when tmp dir is removed")
	}
}

func TestNewLocalWithExplicitKMS(t *testing.T) {
	s, err := NewLocal(LocalConfig{
		Root: t.TempDir(),
		KMS:  &testKMS{},
	})
	if err != nil {
		t.Fatalf("new local with KMS: %v", err)
	}
	if s.enc == nil {
		t.Fatal("encrypter should be non-nil with KMS set")
	}

	ctx := context.Background()
	body := []byte("kms encrypted payload")
	_, err = s.Put(ctx, "kms/test.txt", bytes.NewReader(body), int64(len(body)), PutOptions{})
	if err != nil {
		t.Fatalf("Put with KMS: %v", err)
	}

	rc, _, err := s.Get(ctx, "kms/test.txt")
	if err != nil {
		t.Fatalf("Get with KMS: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("KMS roundtrip mismatch: got %q, want %q", string(got), string(body))
	}
}
