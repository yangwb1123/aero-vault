package storage

import (
	"bytes"
	"context"
	"io"
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
