package storage

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestCloudBackendsRejectInvalidKeysBeforeRemoteCalls(t *testing.T) {
	ctx := context.Background()
	backends := []Storage{
		&S3Storage{},
		&OSSStorage{},
		&COSStorage{},
	}
	for _, backend := range backends {
		t.Run(backend.Backend(), func(t *testing.T) {
			_, err := backend.Put(
				ctx, "/absolute", bytes.NewReader(nil), 0, PutOptions{},
			)
			assertInvalidKeyError(t, err)
			_, _, err = backend.Get(ctx, "../escape")
			assertInvalidKeyError(t, err)
			_, err = backend.Stat(ctx, "")
			assertInvalidKeyError(t, err)
			assertInvalidKeyError(t, backend.Delete(ctx, "/absolute"))
			_, err = backend.List(ctx, "../prefix", "", 10)
			assertInvalidKeyError(t, err)
			_, err = backend.PresignGet(ctx, "/absolute", time.Minute)
			assertInvalidKeyError(t, err)
			_, err = backend.PresignPut(ctx, "../escape", time.Minute)
			assertInvalidKeyError(t, err)
			_, err = backend.InitMultipart(ctx, "/absolute", PutOptions{})
			assertInvalidKeyError(t, err)
			_, err = backend.UploadPart(
				ctx, "../escape", "upload", 1, bytes.NewReader(nil), 0,
			)
			assertInvalidKeyError(t, err)
			_, err = backend.CompleteMultipart(ctx, "/absolute", "upload", nil)
			assertInvalidKeyError(t, err)
			assertInvalidKeyError(t, backend.AbortMultipart(ctx, "../escape", "upload"))
			assertInvalidKeyError(t, backend.CleanupParts(ctx, "/absolute", "upload"))
			_, err = backend.Copy(ctx, "valid", "../escape", CopyOptions{})
			assertInvalidKeyError(t, err)
			_, err = backend.UploadPartCopy(
				ctx, "valid", "upload", 1, "/absolute", -1, 0,
			)
			assertInvalidKeyError(t, err)
		})
	}
}

func assertInvalidKeyError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("error = %v, want ErrInvalidKey", err)
	}
}
