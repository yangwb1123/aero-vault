package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"io"
	"os"
	"testing"
)

func TestLocalSSECCustomerKeyRequiredForRead(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	sum := md5.Sum(key)
	plain := []byte("customer encrypted content")
	info, err := store.Put(ctx, "secret.bin", bytes.NewReader(plain), int64(len(plain)), PutOptions{
		SSECustomerKey: key, SSECustomerKeyMD5: sum[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != int64(len(plain)) {
		t.Fatalf("plaintext size = %d, want %d", info.Size, len(plain))
	}
	path, _ := store.objectPath("secret.bin")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, plain) {
		t.Fatal("SSE-C object was stored in plaintext")
	}

	if _, _, err := store.Get(ctx, "secret.bin"); !errors.Is(err, ErrSSECustomerKeyRequired) {
		t.Fatalf("read without key = %v", err)
	}
	wrong := []byte("abcdef0123456789abcdef0123456789")
	if _, _, err := store.GetWithOptions(ctx, "secret.bin", GetOptions{SSECustomerKey: wrong}); !errors.Is(err, ErrInvalidSSECustomerKey) {
		t.Fatalf("read with wrong key = %v", err)
	}
	rc, gotInfo, err := store.GetWithOptions(ctx, "secret.bin", GetOptions{SSECustomerKey: key, SSECustomerKeyMD5: sum[:]})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decrypted body = %q, err=%v", got, err)
	}
	if gotInfo.Size != int64(len(plain)) {
		t.Fatalf("read size = %d", gotInfo.Size)
	}
}

func TestLocalSSECMultipartRequiresKeyOnEveryStep(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	sum := md5.Sum(key)
	putOpts := PutOptions{SSECustomerKey: key, SSECustomerKeyMD5: sum[:]}
	upload, err := store.InitMultipart(ctx, "multipart.bin", putOpts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UploadPart(ctx, "multipart.bin", upload.UploadID, 1, bytes.NewBufferString("part"), 4); !errors.Is(err, ErrSSECustomerKeyRequired) {
		t.Fatalf("part without key = %v", err)
	}
	part, err := store.UploadPartWithOptions(ctx, "multipart.bin", upload.UploadID, 1, bytes.NewBufferString("part"), 4, putOpts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteMultipart(ctx, "multipart.bin", upload.UploadID, []MultipartPart{part}); !errors.Is(err, ErrSSECustomerKeyRequired) {
		t.Fatalf("complete without key = %v", err)
	}
	if _, err := store.CompleteMultipartWithOptions(ctx, "multipart.bin", upload.UploadID, []MultipartPart{part}, putOpts); err != nil {
		t.Fatal(err)
	}
	rc, _, err := store.GetWithOptions(ctx, "multipart.bin", GetOptions{SSECustomerKey: key})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(body) != "part" {
		t.Fatalf("multipart body = %q", body)
	}
}

func TestLocalRejectsCustomerKeyForServerEncryptedObject(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocal(LocalConfig{Root: t.TempDir(), SSEKey: "server-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "server.bin", bytes.NewBufferString("body"), 4, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	if _, _, err := store.GetWithOptions(ctx, "server.bin", GetOptions{SSECustomerKey: key}); !errors.Is(err, ErrInvalidSSECustomerKey) {
		t.Fatalf("SSE-C key on server-encrypted object = %v", err)
	}
}

func TestLocalMultipartCopyEncryptedSourceRequiresStreamFallback(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocal(LocalConfig{Root: t.TempDir(), SSEKey: "server-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(
		ctx, "source.bin", bytes.NewBufferString("plaintext"), 9, PutOptions{},
	); err != nil {
		t.Fatal(err)
	}
	upload, err := store.InitMultipart(ctx, "copy.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UploadPartCopy(
		ctx, "copy.bin", upload.UploadID, 1, "source.bin", -1, 0,
	); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("encrypted source copy error = %v, want ErrUnsupported", err)
	}
}
