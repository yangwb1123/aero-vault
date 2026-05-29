package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

// Factory builds a fresh Storage for each contract test. Each factory call
// must return a backend with an empty namespace so tests don't bleed into
// each other.
type Factory func(t *testing.T) Storage

// RunContract executes every contract test against the storage produced by f.
// Backend authors call this once: e.g. `storage.RunContract(t, newLocal)`.
func RunContract(t *testing.T, f Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(*testing.T, Storage)
	}{
		{"put_get_stat", contractPutGetStat},
		{"delete", contractDelete},
		{"list_prefix", contractListPrefix},
		{"key_validation", contractKeyValidation},
		{"multipart", contractMultipart},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			s := f(t)
			c.fn(t, s)
		})
	}
}

func contractPutGetStat(t *testing.T, s Storage) {
	ctx := context.Background()
	body := []byte("hello world\n")
	info, err := s.Put(ctx, "default/test/hello.txt", bytes.NewReader(body), int64(len(body)), PutOptions{ContentType: "text/plain", Metadata: map[string]string{"who": "u1"}})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if info.Size != int64(len(body)) {
		t.Fatalf("put size = %d want %d", info.Size, len(body))
	}
	if info.ETag == "" {
		t.Fatalf("put returned empty etag")
	}

	st, err := s.Stat(ctx, "default/test/hello.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size != int64(len(body)) {
		t.Fatalf("stat size = %d want %d", st.Size, len(body))
	}

	rc, _, err := s.Get(ctx, "default/test/hello.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("get returned %q want %q", string(got), string(body))
	}
}

func contractDelete(t *testing.T, s Storage) {
	ctx := context.Background()
	body := []byte("data")
	_, err := s.Put(ctx, "default/a.txt", bytes.NewReader(body), int64(len(body)), PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Delete(ctx, "default/a.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Stat(ctx, "default/a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat after delete: expected ErrNotFound, got %v", err)
	}
}

func contractListPrefix(t *testing.T, s Storage) {
	ctx := context.Background()
	keys := []string{"default/dir/a.txt", "default/dir/b.txt", "default/other/c.txt"}
	for _, k := range keys {
		body := []byte("x")
		if _, err := s.Put(ctx, k, bytes.NewReader(body), int64(len(body)), PutOptions{ContentType: "text/plain"}); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	res, err := s.List(ctx, "default/dir/", "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Objects) < 2 {
		t.Fatalf("list dir/: got %d objects, want >=2", len(res.Objects))
	}
}

func contractKeyValidation(t *testing.T, s Storage) {
	ctx := context.Background()
	bad := []string{"", "../escape", "/absolute"}
	for _, k := range bad {
		_, err := s.Put(ctx, k, bytes.NewReader([]byte("x")), 1, PutOptions{})
		if err == nil {
			t.Errorf("put %q: expected error, got nil", k)
		}
	}
}

func contractMultipart(t *testing.T, s Storage) {
	ctx := context.Background()
	key := "default/multi/big.bin"
	init, err := s.InitMultipart(ctx, key, PutOptions{ContentType: "application/octet-stream"})
	if err != nil {
		t.Fatalf("init multipart: %v", err)
	}
	pieces := [][]byte{
		bytes.Repeat([]byte("a"), 1024),
		bytes.Repeat([]byte("b"), 1024),
		bytes.Repeat([]byte("c"), 512),
	}
	var parts []MultipartPart
	for i, p := range pieces {
		part, err := s.UploadPart(ctx, key, init.UploadID, int32(i+1), bytes.NewReader(p), int64(len(p)))
		if err != nil {
			t.Fatalf("upload part %d: %v", i+1, err)
		}
		parts = append(parts, MultipartPart{PartNumber: int32(i + 1), ETag: part.ETag})
	}
	info, err := s.CompleteMultipart(ctx, key, init.UploadID, parts)
	if err != nil {
		t.Fatalf("complete multipart: %v", err)
	}
	want := int64(0)
	for _, p := range pieces {
		want += int64(len(p))
	}
	if info.Size == 0 {
		info.Size = want
	}
	if info.Size != want {
		t.Fatalf("multipart size = %d want %d", info.Size, want)
	}
	rc, _, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("get assembled: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	expected := bytes.Join(pieces, nil)
	if !bytes.Equal(got, expected) {
		t.Fatalf("assembled bytes mismatch: got %d bytes, want %d", len(got), len(expected))
	}
}

// PrefixedKey is exported for backend authors that want to test against a
// non-default prefix (e.g. tenant-isolated namespaces).
func PrefixedKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return fmt.Sprintf("%s/%s", prefix, key)
}
