package replication

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestReplicateObject(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	primary, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "primary")})
	replica, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "replica")})
	svc := service.NewFileService(primary, repo, nil)

	const content = "replicate me across regions"
	obj, err := svc.Put(ctx, "default", "default", "docs/a.txt", strings.NewReader(content), int64(len(content)), service.PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Replica should not have it yet.
	if _, _, err := replica.Get(ctx, obj.StorageKey); err == nil {
		t.Fatalf("replica unexpectedly has object before replication")
	}

	w := NewWorker(repo, primary, replica, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := w.ReplicateObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("replicate: %v", err)
	}

	// Replica now has identical bytes.
	rc, info, err := replica.Get(ctx, obj.StorageKey)
	if err != nil {
		t.Fatalf("replica get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != content {
		t.Fatalf("replica content=%q want %q", got, content)
	}
	if info.Size != int64(len(content)) {
		t.Fatalf("replica size=%d want %d", info.Size, len(content))
	}

	// Object is tagged replicated.
	reread, _ := repo.GetObject(ctx, "default", "default", "docs/a.txt")
	if reread.Tags[TagStatus] != "replicated" {
		t.Fatalf("expected repl_status=replicated, got %v", reread.Tags)
	}
}

func TestDecodeObjectID(t *testing.T) {
	if _, err := DecodeObjectID(`{"object_id":0}`); err == nil {
		t.Fatal("expected error for missing object_id")
	}
	id, err := DecodeObjectID(EncodeObjectID(42))
	if err != nil || id != 42 {
		t.Fatalf("roundtrip: id=%d err=%v", id, err)
	}
}
