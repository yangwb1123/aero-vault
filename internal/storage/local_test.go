package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalContract(t *testing.T) {
	RunContract(t, func(t *testing.T) Storage {
		dir := t.TempDir()
		s, err := NewLocal(LocalConfig{Root: dir, SignKey: "test-key"})
		if err != nil {
			t.Fatalf("new local: %v", err)
		}
		return s
	})
}

// A failed sidecar write must not leave an orphan object blob behind: without a
// sidecar an SSE ciphertext would be read back as plaintext (raw ciphertext
// served to the client). We force writeMeta to fail by pre-creating a non-empty
// directory at the sidecar path (writeMeta's final rename over it returns EEXIST),
// then assert Put errors AND removes the just-written blob.
func TestPut_OrphanBlobRemovedOnMetaFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir, SSEKey: "test-master-passphrase"})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	key := "default/orphan.txt"
	path, err := s.objectPath(key)
	if err != nil {
		t.Fatalf("objectPath: %v", err)
	}
	// Block writeMeta's final rename: a non-empty dir at the sidecar path can't
	// be replaced by a file rename.
	metaDir := s.metaPath(path)
	if err := os.MkdirAll(filepath.Join(metaDir, "blocker"), 0o755); err != nil {
		t.Fatalf("seed blocking dir: %v", err)
	}

	if _, err := s.Put(context.Background(), key, bytes.NewReader([]byte("secret")), 6, PutOptions{}); err == nil {
		t.Fatal("Put should fail when the sidecar write fails")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("orphan ciphertext blob left behind: stat err = %v", err)
	}
}

// CompleteMultipart shares Put's ordering, so the same orphan-cleanup must apply.
func TestCompleteMultipart_OrphanBlobRemovedOnMetaFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(LocalConfig{Root: dir, SSEKey: "test-master-passphrase"})
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	key := "default/mp-orphan.txt"
	dst, err := s.objectPath(key)
	if err != nil {
		t.Fatalf("objectPath: %v", err)
	}
	// objectPath does not create the dir; do it so we can plant the blocker.
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir dst dir: %v", err)
	}
	metaDir := s.metaPath(dst)
	if err := os.MkdirAll(filepath.Join(metaDir, "blocker"), 0o755); err != nil {
		t.Fatalf("seed blocking dir: %v", err)
	}

	init, err := s.InitMultipart(ctx, key, PutOptions{})
	if err != nil {
		t.Fatalf("init multipart: %v", err)
	}
	part, err := s.UploadPart(ctx, key, init.UploadID, 1, bytes.NewReader([]byte("secret")), 6)
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	if _, err := s.CompleteMultipart(ctx, key, init.UploadID, []MultipartPart{part}); err == nil {
		t.Fatal("CompleteMultipart should fail when the sidecar write fails")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("orphan ciphertext blob left behind: stat err = %v", err)
	}
}
