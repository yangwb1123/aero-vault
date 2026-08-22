package storage

import (
	"bytes"
	"context"
	"testing"
)

func TestLocalStorageCopyUsesSourceEnvelopeForPlaintext(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	plain, err := NewLocal(LocalConfig{Root: root})
	if err != nil {
		t.Fatalf("new plaintext store: %v", err)
	}
	const body = "hello"
	if _, err := plain.Put(ctx, "plain", bytes.NewBufferString(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatalf("put plaintext object: %v", err)
	}

	withSSE, err := NewLocal(LocalConfig{Root: root, SSEKey: "copy-test-key"})
	if err != nil {
		t.Fatalf("new sse store: %v", err)
	}
	info, err := withSSE.Copy(ctx, "plain", "copy", CopyOptions{})
	if err != nil {
		t.Fatalf("copy plaintext object: %v", err)
	}
	if info.Size != int64(len(body)) {
		t.Fatalf("copied plaintext size = %d, want %d", info.Size, len(body))
	}
	stat, err := withSSE.Stat(ctx, "copy")
	if err != nil {
		t.Fatalf("stat copied plaintext object: %v", err)
	}
	if stat.Size != int64(len(body)) {
		t.Fatalf("stored plaintext size = %d, want %d", stat.Size, len(body))
	}
}

func TestLocalStorageCopyUsesSourceEnvelopeForEncrypted(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	encrypted, err := NewLocal(LocalConfig{Root: root, SSEKey: "copy-test-key"})
	if err != nil {
		t.Fatalf("new encrypted store: %v", err)
	}
	const body = "hello"
	if _, err := encrypted.Put(ctx, "encrypted", bytes.NewBufferString(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatalf("put encrypted object: %v", err)
	}

	withoutSSE, err := NewLocal(LocalConfig{Root: root})
	if err != nil {
		t.Fatalf("new non-sse store: %v", err)
	}
	info, err := withoutSSE.Copy(ctx, "encrypted", "copy", CopyOptions{})
	if err != nil {
		t.Fatalf("copy encrypted object: %v", err)
	}
	if info.Size != int64(len(body)) {
		t.Fatalf("copied encrypted size = %d, want %d", info.Size, len(body))
	}
	stat, err := withoutSSE.Stat(ctx, "copy")
	if err != nil {
		t.Fatalf("stat copied encrypted object: %v", err)
	}
	if stat.Size != int64(len(body)) {
		t.Fatalf("stored encrypted size = %d, want %d", stat.Size, len(body))
	}
}
