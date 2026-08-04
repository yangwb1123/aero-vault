package rest

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestSSEReplayIncludesConsumedEventsAcrossPages(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}

	firstID := insertReplayEvent(t, repo, "tenant-a", "first")
	if err := repo.MarkEventConsumed(ctx, firstID); err != nil {
		t.Fatalf("consume first event: %v", err)
	}
	lastID := firstID
	for i := 0; i < 205; i++ {
		lastID = insertReplayEvent(t, repo, "tenant-a", "page")
		if err := repo.MarkEventConsumed(ctx, lastID); err != nil {
			t.Fatalf("consume event: %v", err)
		}
	}
	otherID := insertReplayEvent(t, repo, "tenant-b", "hidden")

	handler := NewSSEHandler(nil, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/events/stream", nil)
	replayedThrough := handler.replayMissed(rec, rec, req, "tenant-a", firstID)

	if replayedThrough != lastID {
		t.Fatalf("replayed through %d, want %d", replayedThrough, lastID)
	}
	body := rec.Body.String()
	if got := strings.Count(body, "id: "); got != 205 {
		t.Fatalf("replayed %d events, want 205", got)
	}
	if strings.Contains(body, "id: "+strconv.FormatInt(otherID, 10)+"\n") {
		t.Fatalf("cross-tenant event %d was replayed", otherID)
	}
}

func insertReplayEvent(t *testing.T, repo repository.Repository, tenant, key string) int64 {
	t.Helper()
	id, err := repo.InsertEvent(context.Background(), repository.Event{
		TenantID: tenant,
		Bucket:   "bucket",
		Key:      key,
		Type:     repository.EventCreated,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return id
}
