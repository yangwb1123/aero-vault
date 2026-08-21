package replication

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestReplicateSSECSkip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "ssec.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	primary, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "primary")})
	if err != nil {
		t.Fatalf("primary: %v", err)
	}
	replica, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "replica")})
	if err != nil {
		t.Fatalf("replica: %v", err)
	}
	svc := service.NewFileService(primary, repo, nil)
	key := []byte("0123456789abcdef0123456789abcdef")
	obj, err := svc.Put(ctx, "default", "default", "ssec.txt", strings.NewReader("secret"), 6,
		service.PutOptions{SSECustomerKey: key})
	if err != nil {
		t.Fatalf("put SSE-C object: %v", err)
	}
	worker := NewWorker(repo, primary, replica, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectTagger(svc)
	if err := worker.ReplicateObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("SSE-C replication should skip successfully: %v", err)
	}
	got, err := repo.GetObjectByID(ctx, obj.ID)
	if err != nil {
		t.Fatalf("reload object: %v", err)
	}
	if got.Tags[TagStatus] != "skipped" {
		t.Fatalf("repl_status = %q, want skipped", got.Tags[TagStatus])
	}
	if _, _, err := replica.Get(ctx, obj.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("SSE-C skip touched replica: err=%v", err)
	}
}

func TestReplicateSSECSkipTagFailureIsNonFatal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "ssec.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	primary, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "primary")})
	if err != nil {
		t.Fatalf("primary: %v", err)
	}
	replica, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "replica")})
	if err != nil {
		t.Fatalf("replica: %v", err)
	}
	svc := service.NewFileService(primary, repo, nil)
	obj, err := svc.Put(ctx, "", "", "ssec.txt", strings.NewReader("secret"), 6,
		service.PutOptions{SSECustomerKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatalf("put SSE-C object: %v", err)
	}
	worker := NewWorker(repo, primary, replica, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectTagger(failingTagger{})
	if err := worker.ReplicateObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("tag failure must not fail SSE-C skip: %v", err)
	}
	if _, _, err := replica.Get(ctx, obj.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("SSE-C skip touched replica: err=%v", err)
	}
}

type failingTagger struct{}

func (failingTagger) SetObjectTagsByID(context.Context, int64, map[string]string) error {
	return errors.New("tag store unavailable")
}
