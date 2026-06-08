package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"strings"
	"testing"
)

// A completed multipart object reports the AWS-style composite ETag
// (<hex md5 of concatenated part MD5s>-<partCount>), not a whole-object MD5.
func TestLocalMultipart_CompositeETag(t *testing.T) {
	ctx := context.Background()
	s, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	const key = "default/m.bin"
	init, err := s.InitMultipart(ctx, key, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p1, err := s.UploadPart(ctx, key, init.UploadID, 1, bytes.NewReader([]byte("AAAA")), 4)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.UploadPart(ctx, key, init.UploadID, 2, bytes.NewReader([]byte("BBBB")), 4)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.CompleteMultipart(ctx, key, init.UploadID, []MultipartPart{p1, p2})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(info.ETag, "-2") {
		t.Fatalf("multipart ETag should end with -2 (part count), got %q", info.ETag)
	}
	hashPart, _, _ := strings.Cut(info.ETag, "-")
	if len(hashPart) != 32 {
		t.Fatalf("multipart ETag hash should be 32 hex chars, got %q", info.ETag)
	}
	// It must NOT be the whole-object MD5.
	whole := md5.Sum([]byte("AAAABBBB"))
	if hashPart == hex.EncodeToString(whole[:]) {
		t.Fatal("multipart ETag should be the composite, not the whole-object MD5")
	}
	// It MUST equal md5(part1_md5_binary || part2_md5_binary).
	a := md5.Sum([]byte("AAAA"))
	b := md5.Sum([]byte("BBBB"))
	want := md5.Sum(append(append([]byte{}, a[:]...), b[:]...))
	if hashPart != hex.EncodeToString(want[:]) {
		t.Fatalf("composite ETag mismatch: got %s want %s", hashPart, hex.EncodeToString(want[:]))
	}
}
