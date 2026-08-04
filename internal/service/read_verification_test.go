package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadVerificationAcceptsBase64ContentMD5(t *testing.T) {
	svc, _ := newTestSvc(t)
	svc.WithReadVerification(ReadVerificationConfig{Enabled: true})
	body := []byte("verified content")
	digest := md5.Sum(body)

	if _, err := svc.Put(
		context.Background(), "", "", "verified.txt", bytes.NewReader(body), int64(len(body)),
		PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])},
	); err != nil {
		t.Fatalf("put: %v", err)
	}

	rc, _, err := svc.Get(context.Background(), "", "", "verified.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close verified reader: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestReadVerificationSkipsMultipartETag(t *testing.T) {
	svc, _ := newTestSvc(t)
	svc.WithReadVerification(ReadVerificationConfig{Enabled: true})
	ctx := context.Background()

	upload, err := svc.InitMultipart(ctx, "", "", "multipart.txt", PutOptions{})
	if err != nil {
		t.Fatalf("init multipart: %v", err)
	}
	if _, err := svc.UploadPart(ctx, upload.ID, 1, strings.NewReader("part body"), 9); err != nil {
		t.Fatalf("upload part: %v", err)
	}
	obj, err := svc.CompleteMultipart(ctx, upload.ID)
	if err != nil {
		t.Fatalf("complete multipart: %v", err)
	}
	if !strings.Contains(obj.ETag, "-") {
		t.Fatalf("multipart ETag %q has no part suffix", obj.ETag)
	}

	rc, _, err := svc.Get(ctx, "", "", "multipart.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("multipart read must not be treated as corrupt: %v", err)
	}
}

func TestReadVerificationTreatsContentEncodingAsOpaqueBytes(t *testing.T) {
	svc, _ := newTestSvc(t)
	svc.WithReadVerification(ReadVerificationConfig{Enabled: true})
	body := []byte("compressed and verified")
	var encoded bytes.Buffer
	zw := gzip.NewWriter(&encoded)
	if _, err := zw.Write(body); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	digest := md5.Sum(encoded.Bytes())

	if _, err := svc.Put(
		context.Background(), "", "", "compressed.txt",
		bytes.NewReader(encoded.Bytes()), int64(encoded.Len()),
		PutOptions{
			ContentMD5:      base64.StdEncoding.EncodeToString(digest[:]),
			ContentEncoding: "gzip",
		},
	); err != nil {
		t.Fatalf("put: %v", err)
	}

	rc, _, err := svc.Get(context.Background(), "", "", "compressed.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !bytes.Equal(got, encoded.Bytes()) {
		t.Fatalf("body differs from uploaded encoded representation")
	}
}

func TestETagVerifierDetectsCorruptionOnlyAfterCompleteRead(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		rc := NewETagVerifier(
			io.NopCloser(strings.NewReader("corrupt")),
			hex.EncodeToString(make([]byte, md5.Size)),
		)
		if _, err := io.ReadAll(rc); !errors.Is(err, ErrObjectCorrupt) {
			t.Fatalf("read error = %v, want ErrObjectCorrupt", err)
		}
		if err := rc.Close(); !errors.Is(err, ErrObjectCorrupt) {
			t.Fatalf("close error = %v, want ErrObjectCorrupt", err)
		}
	})

	t.Run("partial", func(t *testing.T) {
		rc := NewETagVerifier(
			io.NopCloser(strings.NewReader("healthy but partially consumed")),
			hex.EncodeToString(make([]byte, md5.Size)),
		)
		if _, err := rc.Read(make([]byte, 1)); err != nil {
			t.Fatalf("partial read: %v", err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("partial close must not report corruption: %v", err)
		}
	})
}
