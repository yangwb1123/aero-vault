package billing

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type retryCall struct {
	maxAttempts int
	lastErr     string
}

type recordingBillingStore struct {
	Store
	calls []retryCall
}

func (s *recordingBillingStore) RetryBillingUsage(
	ctx context.Context, id, owner, lastErr string, next time.Time, maxAttempts int,
) error {
	s.calls = append(s.calls, retryCall{maxAttempts: maxAttempts, lastErr: lastErr})
	return s.Store.RetryBillingUsage(ctx, id, owner, lastErr, next, maxAttempts)
}

func failingUsageClient(t *testing.T, status int) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathUsage {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":"billing failure"}`)
	}))
	client := newClient(server.URL, server.Client(), &tokenSource{
		client: &fakeCredentialsClient{}, now: time.Now,
	})
	return client, server.Close
}

func outboxRuntime(store Store, client *Client, maxAttempts int) *Runtime {
	return &Runtime{
		store: store, bindings: map[string]*Client{"acme": client},
		maxAttempts: maxAttempts, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func seedBillingFact(t *testing.T, store Store, operation string) repository.BillingUsageFact {
	t.Helper()
	ctx := context.Background()
	if _, _, err := store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
		OperationID: operation, TenantID: "acme", Kind: "object_write",
		DeltaBytes: 1, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("apply billing usage: %v", err)
	}
	facts, err := store.ClaimBillingUsage(ctx, "seed-worker", 10, time.Minute)
	if err != nil || len(facts) != 1 {
		t.Fatalf("claim billing fact: len=%d err=%v", len(facts), err)
	}
	return facts[0]
}

func TestOutboxMaxAttemptsExcludesFactFromClaim(t *testing.T) {
	ctx := context.Background()
	_, base := openRuntimeTestStore(t)
	recording := &recordingBillingStore{Store: base}
	client, closeServer := failingUsageClient(t, http.StatusInternalServerError)
	t.Cleanup(closeServer)
	runtime := outboxRuntime(recording, client, 2)
	fact := seedBillingFact(t, recording, "outbox-cap")
	if err := recording.RetryBillingUsage(ctx, fact.ID, fact.ClaimOwner, "first failure",
		time.Now().Add(-time.Minute), 99); err != nil {
		t.Fatalf("force first retry: %v", err)
	}
	facts, err := recording.ClaimBillingUsage(ctx, "second-worker", 10, time.Minute)
	if err != nil || len(facts) != 1 || facts[0].Attempts != 2 {
		t.Fatalf("claim at cap: facts=%v err=%v", facts, err)
	}
	runtime.deliverFact(ctx, facts[0])
	if len(recording.calls) < 2 || recording.calls[len(recording.calls)-1].maxAttempts != 2 {
		t.Fatalf("retry calls=%+v, final maxAttempts want 2", recording.calls)
	}
	if recording.calls[len(recording.calls)-1].lastErr == "" {
		t.Fatal("terminal retry lost last error")
	}
	if again, err := recording.ClaimBillingUsage(ctx, "third-worker", 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("terminal fact was claimable: facts=%v err=%v", again, err)
	}
}

func TestOutboxHTTP4xxIsTerminalWithLastError(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		t.Run(statusText(status), func(t *testing.T) {
			ctx := context.Background()
			_, base := openRuntimeTestStore(t)
			recording := &recordingBillingStore{Store: base}
			client, closeServer := failingUsageClient(t, status)
			t.Cleanup(closeServer)
			runtime := outboxRuntime(recording, client, 10)
			fact := seedBillingFact(t, recording, "outbox-4xx-"+statusText(status))
			runtime.deliverFact(ctx, fact)
			last := recording.calls[len(recording.calls)-1]
			if last.maxAttempts != fact.Attempts || last.lastErr == "" {
				t.Fatalf("terminal call=%+v, want maxAttempts=%d and error", last, fact.Attempts)
			}
			if again, err := recording.ClaimBillingUsage(ctx, "worker", 10, time.Minute); err != nil || len(again) != 0 {
				t.Fatalf("4xx fact was claimable: facts=%v err=%v", again, err)
			}
		})
	}

	t.Run("500-retries", func(t *testing.T) {
		ctx := context.Background()
		_, base := openRuntimeTestStore(t)
		recording := &recordingBillingStore{Store: base}
		client, closeServer := failingUsageClient(t, http.StatusInternalServerError)
		t.Cleanup(closeServer)
		runtime := outboxRuntime(recording, client, 10)
		fact := seedBillingFact(t, recording, "outbox-500")
		runtime.deliverFact(ctx, fact)
		last := recording.calls[len(recording.calls)-1]
		if last.maxAttempts != 10 {
			t.Fatalf("transient call=%+v, want runtime cap 10", last)
		}
	})
}

func TestOutboxPoisonPillConvergesAtCap(t *testing.T) {
	ctx := context.Background()
	_, base := openRuntimeTestStore(t)
	recording := &recordingBillingStore{Store: base}
	client, closeServer := failingUsageClient(t, http.StatusInternalServerError)
	t.Cleanup(closeServer)
	runtime := outboxRuntime(recording, client, 3)
	fact := seedBillingFact(t, recording, "outbox-poison")
	for attempt := 1; attempt <= 2; attempt++ {
		if err := recording.RetryBillingUsage(ctx, fact.ID, fact.ClaimOwner, "transient",
			time.Now().Add(-time.Minute), 99); err != nil {
			t.Fatalf("retry %d: %v", attempt, err)
		}
		facts, err := recording.ClaimBillingUsage(ctx, "poison-worker", 10, time.Minute)
		if err != nil || len(facts) != 1 {
			t.Fatalf("claim %d: facts=%v err=%v", attempt+1, facts, err)
		}
		fact = facts[0]
	}
	runtime.deliverFact(ctx, fact)
	if last := recording.calls[len(recording.calls)-1]; last.maxAttempts != 3 {
		t.Fatalf("poison terminal call=%+v, want maxAttempts=3", last)
	}
	if facts, err := recording.ClaimBillingUsage(ctx, "after-poison", 10, time.Minute); err != nil || len(facts) != 0 {
		t.Fatalf("poison pill remained claimable: facts=%v err=%v", facts, err)
	}
}
