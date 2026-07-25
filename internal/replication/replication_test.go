package replication

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/jobs"
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

func TestEncodeObjectID(t *testing.T) {
	payload := EncodeObjectID(42)
	if payload == "" {
		t.Fatal("expected non-empty payload")
	}
	var v struct {
		ObjectID int64 `json:"object_id"`
	}
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.ObjectID != 42 {
		t.Fatalf("expected object_id=42, got %d", v.ObjectID)
	}
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "store")})
	replica, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "replica")})
	q := jobs.NewQueue(repo)
	w := NewWorker(repo, store, replica, q, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ch := make(chan repository.Event, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.Run(ctx, ch)
	}()

	objectID := int64(1)
	ch <- repository.Event{
		Type:     repository.EventCreated,
		ObjectID: &objectID,
		TenantID: "default",
	}
	close(ch)
	wg.Wait()

	jobs, err := repo.ListJobs(ctx, "pending", JobReplicate, 10)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Type != JobReplicate {
		t.Fatalf("expected type %q, got %q", JobReplicate, jobs[0].Type)
	}
	if jobs[0].TenantID != "default" {
		t.Fatalf("expected tenant default, got %q", jobs[0].TenantID)
	}
}
