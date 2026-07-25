package events

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// retryOne must not panic on a malformed URL: http.NewRequest returns an error and
// a nil request, so the old `req, _ :=` would nil-deref on req.Header.Set. The fix
// records the failed attempt and returns instead.
func TestWebhook_RetryOne_MalformedURLDoesNotPanic(t *testing.T) {
	w := &Webhook{client: &http.Client{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// No repo wired → persistFailure just logs; the assertion is simply: no panic.
	w.retryOne(context.Background(), repository.WebhookFailure{EventID: 1, URL: "://not a url", Payload: "{}"})
}

// terminalRepo records calls to the two terminal-transition methods so the test
// can assert how a maxed-out failure is retired. It embeds the full Repository so
// it satisfies the type; any other method call panics (must not happen here).
type terminalRepo struct {
	repository.Repository // nil embedded interface: any unexpected call panics

	mu         sync.Mutex
	markedDone []int64              // ids passed to MarkWebhookSucceeded
	updates    []terminalRepoUpdate // args passed to UpdateWebhookFailure
}

type terminalRepoUpdate struct {
	id          int64
	lastErr     string
	nextRetryAt time.Time
	attempts    int
}

func (r *terminalRepo) MarkWebhookSucceeded(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markedDone = append(r.markedDone, id)
	return nil
}

func (r *terminalRepo) UpdateWebhookFailure(_ context.Context, id int64, lastErr string, _ int, nextRetryAt time.Time, attempts int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, terminalRepoUpdate{id: id, lastErr: lastErr, nextRetryAt: nextRetryAt, attempts: attempts})
	return nil
}

// After the max-attempts threshold a still-failing delivery must be retired so
// NextPendingFailures (WHERE succeeded = 0) stops re-selecting it; otherwise the
// dead-letter table grows unbounded and the row retries forever. Since the schema
// has only a binary `succeeded` flag, the terminal transition is MarkWebhookSucceeded.
func TestWebhook_RetryOne_MaxAttemptsRetiresRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := &terminalRepo{}
	w := &Webhook{
		client: srv.Client(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo:   repo,
	}

	// Attempts=9 → this delivery is attempt 10, which trips the terminal branch.
	w.retryOne(context.Background(), repository.WebhookFailure{ID: 7, EventID: 1, URL: srv.URL, Payload: "{}", Attempts: 9})

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if len(repo.markedDone) != 1 || repo.markedDone[0] != 7 {
		t.Fatalf("expected MarkWebhookSucceeded(7) to retire the maxed-out row, got markedDone=%v", repo.markedDone)
	}
	if len(repo.updates) != 1 {
		t.Fatalf("expected exactly one UpdateWebhookFailure recording the final error, got %d", len(repo.updates))
	}
	last := repo.updates[0]
	if last.attempts != 10 {
		t.Fatalf("expected final attempts=10, got %d", last.attempts)
	}
	// Must not schedule a future retry — the old bug parked next_retry_at 24h out
	// while leaving succeeded=0, so the row was re-selected every 24h forever.
	if last.nextRetryAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("terminal failure must not schedule a future retry, got next_retry_at=%s", last.nextRetryAt)
	}
}

// Below the max-attempts threshold a failing delivery must be rescheduled (a
// future next_retry_at) and must NOT be retired.
func TestWebhook_RetryOne_BelowMaxReschedules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := &terminalRepo{}
	w := &Webhook{
		client: srv.Client(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo:   repo,
	}

	// Attempts=2 → attempt 3, well below the threshold.
	w.retryOne(context.Background(), repository.WebhookFailure{ID: 3, EventID: 1, URL: srv.URL, Payload: "{}", Attempts: 2})

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if len(repo.markedDone) != 0 {
		t.Fatalf("a below-threshold failure must not be retired, got markedDone=%v", repo.markedDone)
	}
	if len(repo.updates) != 1 {
		t.Fatalf("expected one UpdateWebhookFailure rescheduling the retry, got %d", len(repo.updates))
	}
	if !repo.updates[0].nextRetryAt.After(time.Now()) {
		t.Fatalf("below-threshold failure must reschedule a future retry, got next_retry_at=%s", repo.updates[0].nextRetryAt)
	}
}
