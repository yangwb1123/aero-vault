package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestQueue_MaxDepthBackpressure verifies Enqueue returns ErrQueueFull once the
// pending backlog reaches the configured cap, and accepts again after drain.
func TestQueue_MaxDepthBackpressure(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	q := NewQueue(repo).WithMaxDepth(2)

	for i := 0; i < 2; i++ {
		if _, _, err := q.Enqueue(ctx, repository.Job{Type: "t", Payload: "{}"}); err != nil {
			t.Fatalf("enqueue %d should succeed: %v", i, err)
		}
	}
	// Third enqueue exceeds the cap.
	if _, _, err := q.Enqueue(ctx, repository.Job{Type: "t", Payload: "{}"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull at cap, got %v", err)
	}

	// Unbounded queue never rejects.
	qu := NewQueue(repo)
	if _, _, err := qu.Enqueue(ctx, repository.Job{Type: "t", Payload: "{}"}); err != nil {
		t.Fatalf("unbounded enqueue should succeed: %v", err)
	}
}
