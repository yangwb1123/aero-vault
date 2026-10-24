package events

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func TestOutboxBoundary_CapabilityAndObservationFaultsRetry(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repo repository.Repository, relay *EventOutboxRelay, spy *relayRepoSpy)
	}{
		{name: "missing discovered capability", setup: func(t *testing.T, repo repository.Repository, relay *EventOutboxRelay, spy *relayRepoSpy) {
			// The spy embeds Repository but not BindingReader, so construction
			// discovers no capability and must not default to unbound.
			if spy == nil {
				t.Fatal("missing spy")
			}
		}},
		{name: "explicit nil capability", setup: func(t *testing.T, _ repository.Repository, relay *EventOutboxRelay, _ *relayRepoSpy) {
			relay.WithResourceBoundary(nil)
		}},
		{name: "typed nil capability", setup: func(t *testing.T, _ repository.Repository, relay *EventOutboxRelay, _ *relayRepoSpy) {
			var reader *relayBoundReader
			relay.WithResourceBoundary(reader)
		}},
		{name: "invalid binding echo", setup: func(t *testing.T, _ repository.Repository, relay *EventOutboxRelay, _ *relayRepoSpy) {
			reader := newRelayBoundReader()
			reader.setResult("default", "default", relayUnboundResult("other", "default"))
			relay.WithResourceBoundary(reader)
		}},
		{name: "malformed bound result", setup: func(t *testing.T, _ repository.Repository, relay *EventOutboxRelay, _ *relayRepoSpy) {
			reader := newRelayBoundReader()
			reader.setResult("default", "default", spaces.BindingResult{
				TenantID: "default", Bucket: "default", TenantKnown: true,
				TenantStatus: "active", State: spaces.BindingBound, Revision: 1,
			})
			relay.WithResourceBoundary(reader)
		}},
		{name: "binding read fault", setup: func(t *testing.T, _ repository.Repository, relay *EventOutboxRelay, _ *relayRepoSpy) {
			reader := newRelayBoundReader()
			reader.setErr(context.DeadlineExceeded)
			relay.WithResourceBoundary(reader)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := "file:" + filepath.Join(t.TempDir(), "boundary-fault.db")
			repo := openRelayTestRepoAt(t, dsn)
			obj := seedRelayObject(t, repo, "boundary/fault.txt")
			seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
			target := &captureTarget{}
			srv := newRelayTargetServer(t, target)
			defer srv.Close()
			setDeleteRule(t, repo, srv.URL)
			spy := &relayRepoSpy{Repository: repo}
			if tc.name == "missing discovered capability" {
				relay := NewEventOutboxRelay(spy, nil, EventOutboxRelayOptions{
					PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
				})
				tc.setup(t, repo, relay, spy)
				assertBoundaryFaultRetry(t, dsn, repo, relay, spy, target, obj)
				return
			}
			relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
				PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
			})
			tc.setup(t, repo, relay, spy)
			assertBoundaryFaultRetry(t, dsn, repo, relay, spy, target, obj)
		})
	}
}

func newRelayTargetServer(t *testing.T, target *captureTarget) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(target.handler))
}

func assertBoundaryFaultRetry(
	t *testing.T, dsn string, repo repository.Repository, relay *EventOutboxRelay,
	spy *relayRepoSpy, target *captureTarget, obj repository.Object,
) {
	t.Helper()
	fact := claimSingleRelayFact(t, repo, "owner-fault", "token-fault")
	relay.deliverFact(fact)
	if got := target.count(); got != 0 {
		t.Fatalf("boundary fault produced %d POSTs, want 0", got)
	}
	state := readRelayFactState(t, dsn, obj.ID, repository.EventTypeFileNotify11)
	requireRelayFactPending(t, "boundary fault", state)
	if state.Attempts != 1 {
		t.Fatalf("boundary fault attempts = %d, want 1", state.Attempts)
	}
	if got := len(spy.completeRecords()); got != 0 {
		t.Fatalf("boundary fault completed %d facts, want 0", got)
	}
}

func TestOutboxBoundary_ExhaustsFiniteRetriesThenFails(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "boundary-exhaustion.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "boundary/exhaustion.txt")
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
	target := &captureTarget{}
	srv := newRelayTargetServer(t, target)
	defer srv.Close()
	setDeleteRule(t, repo, srv.URL)
	reader := newRelayBoundReader()
	reader.setErr(errors.New("binding database unavailable"))
	relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 2,
	})
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-exhaust-1", "token-exhaust-1")
	relay.deliverFact(fact)
	state := readRelayFactState(t, dsn, obj.ID, repository.EventTypeFileNotify11)
	requireRelayFactPending(t, "first boundary fault", state)

	// Advance only the persisted availability clock; status, attempts and
	// last_error remain exactly as the relay wrote them.
	backdateEventOutboxAt(t, dsn, "available_at_ns", obj.ID, time.Now().Add(-time.Minute))
	second := claimSingleRelayFact(t, repo, "owner-exhaust-2", "token-exhaust-2")
	relay.deliverFact(second)
	state = readRelayFactState(t, dsn, obj.ID, repository.EventTypeFileNotify11)
	requireRelayFactFailed(t, "exhausted boundary fault", state)
	if state.Attempts != 2 {
		t.Fatalf("exhausted attempts = %d, want 2", state.Attempts)
	}
	if got := target.count(); got != 0 {
		t.Fatalf("exhaustion produced %d POSTs, want 0", got)
	}
	if pending, err := repo.ClaimEventOutbox(context.Background(), "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
		t.Fatalf("exhausted fact reclaimable: len=%d err=%v", len(pending), err)
	}
}

func TestOutboxBoundary_FencedWritesForwardExactOwnerAndToken(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "boundary-fencing.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "boundary/fencing.txt")
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
	target := &captureTarget{}
	srv := newRelayTargetServer(t, target)
	defer srv.Close()
	setDeleteRule(t, repo, srv.URL)
	spy := &relayRepoSpy{Repository: repo}
	reader := newRelayBoundReader()
	reader.setBound("default", "default")
	relay := NewEventOutboxRelay(spy, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
	})
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-exact", "token-exact")
	relay.deliverFact(fact)
	records := spy.retryRecords()
	if len(records) != 1 {
		t.Fatalf("policy refusal retry writes = %d, want 1", len(records))
	}
	if records[0].ID != fact.ID || records[0].Owner != fact.ClaimOwner || records[0].Token != fact.ClaimToken {
		t.Fatalf("fenced retry arguments changed: %+v fact=%+v", records[0], fact)
	}
	if records[0].MaxAttempts != fact.Attempts {
		t.Fatalf("policy maxAttempts = %d, want fact attempts %d", records[0].MaxAttempts, fact.Attempts)
	}
	if got := len(spy.completeRecords()); got != 0 {
		t.Fatalf("policy refusal completed %d facts, want 0", got)
	}
	state := readRelayFactState(t, dsn, obj.ID, repository.EventTypeFileNotify11)
	requireRelayFactFailed(t, "policy exact fencing", state)
}

func TestOutboxBoundary_PersistenceFailuresDoNotLoopOrClaimSuccess(t *testing.T) {
	t.Run("terminal policy write failure", func(t *testing.T) {
		dsn := "file:" + filepath.Join(t.TempDir(), "boundary-policy-persist.db")
		repo := openRelayTestRepoAt(t, dsn)
		obj := seedRelayObject(t, repo, "boundary/policy-persist.txt")
		seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
		target := &captureTarget{}
		srv := newRelayTargetServer(t, target)
		defer srv.Close()
		setDeleteRule(t, repo, srv.URL)
		spy := &relayRepoSpy{Repository: repo}
		spy.setRetryErr(errors.New("retry store unavailable"))
		reader := newRelayBoundReader()
		reader.setBound("default", "default")
		relay := NewEventOutboxRelay(spy, nil, EventOutboxRelayOptions{
			PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
		})
		relay.WithResourceBoundary(reader)

		fact := claimSingleRelayFact(t, repo, "owner-policy-persist", "token-policy-persist")
		relay.deliverFact(fact)
		records := spy.retryRecords()
		if len(records) != 1 {
			t.Fatalf("policy persistence failure wrote %d retries, want exactly 1", len(records))
		}
		if records[0].Owner != fact.ClaimOwner || records[0].Token != fact.ClaimToken {
			t.Fatalf("policy persistence fencing changed: %+v fact=%+v", records[0], fact)
		}
		if got := len(spy.completeRecords()); got != 0 {
			t.Fatalf("failed policy persistence completed %d facts, want 0", got)
		}
		state := readRelayFactState(t, dsn, obj.ID, repository.EventTypeFileNotify11)
		requireRelayFactNotDelivered(t, "failed policy persistence", state)
		if state.Status != "inflight" {
			t.Fatalf("failed policy persistence status = %s, want inflight", state.Status)
		}
	})

	t.Run("ordinary retry persistence failure", func(t *testing.T) {
		dsn := "file:" + filepath.Join(t.TempDir(), "boundary-retry-persist.db")
		repo := openRelayTestRepoAt(t, dsn)
		obj := seedRelayObject(t, repo, "boundary/retry-persist.txt")
		payload := mutateOutboxNotifyPayload(t, fixedNotifyPayload(obj), func(doc map[string]any) {
			doc["tenant"] = "other"
		})
		seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, payload)
		spy := &relayRepoSpy{Repository: repo}
		spy.setRetryErr(errors.New("retry store unavailable"))
		relay := NewEventOutboxRelay(spy, nil, EventOutboxRelayOptions{
			PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
		})

		fact := claimSingleRelayFact(t, repo, "owner-retry-persist", "token-retry-persist")
		relay.deliverFact(fact)
		records := spy.retryRecords()
		if len(records) != 1 {
			t.Fatalf("retry persistence failure wrote %d retries, want exactly 1", len(records))
		}
		if records[0].Owner != fact.ClaimOwner || records[0].Token != fact.ClaimToken {
			t.Fatalf("retry persistence fencing changed: %+v fact=%+v", records[0], fact)
		}
		state := readRelayFactState(t, dsn, obj.ID, repository.EventTypeFileNotify11)
		requireRelayFactNotDelivered(t, "failed retry persistence", state)
	})

	t.Run("completion persistence failure", func(t *testing.T) {
		dsn := "file:" + filepath.Join(t.TempDir(), "boundary-complete-persist.db")
		repo := openRelayTestRepoAt(t, dsn)
		obj := seedRelayObject(t, repo, "boundary/complete-persist.txt")
		seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
		target := &captureTarget{}
		srv := newRelayTargetServer(t, target)
		defer srv.Close()
		setDeleteRule(t, repo, srv.URL)
		spy := &relayRepoSpy{Repository: repo}
		spy.completeErr = errors.New("complete store unavailable")
		reader := newRelayBoundReader()
		relay := NewEventOutboxRelay(spy, nil, EventOutboxRelayOptions{
			PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
		})
		relay.WithResourceBoundary(reader)

		fact := claimSingleRelayFact(t, repo, "owner-complete-persist", "token-complete-persist")
		relay.deliverFact(fact)
		completes := spy.completeRecords()
		if len(completes) != 1 {
			t.Fatalf("completion persistence failure wrote %d completes, want exactly 1", len(completes))
		}
		if completes[0].ID != fact.ID || completes[0].Owner != fact.ClaimOwner || completes[0].Token != fact.ClaimToken {
			t.Fatalf("completion fencing changed: %+v fact=%+v", completes[0], fact)
		}
		state := readRelayFactState(t, dsn, obj.ID, repository.EventTypeFileNotify11)
		requireRelayFactNotDelivered(t, "failed completion persistence", state)
		if state.Status != "inflight" {
			t.Fatalf("failed completion persistence status = %s, want inflight", state.Status)
		}
	})
}

func TestOutboxBoundary_ContextAndReaderFaultsPreserveCause(t *testing.T) {
	boundary := newOutboxBoundary(newRelayBoundReader())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := boundary.check(ctx, "default", "default")
	if !errors.Is(err, errOutboxBoundaryFault) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context fault = %v, want boundary fault with context.Canceled cause", err)
	}

	reader := newRelayBoundReader()
	reader.setErr(spaces.ErrPersistence)
	boundary = newOutboxBoundary(reader)
	err = boundary.check(context.Background(), "default", "default")
	if !errors.Is(err, errOutboxBoundaryFault) || !errors.Is(err, spaces.ErrPersistence) {
		t.Fatalf("reader fault = %v, want boundary fault with persistence cause", err)
	}
}

func TestOutboxBoundary_ClaimLossIsAttemptedOnce(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "boundary-claim-loss.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "boundary/claim-loss.txt")
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
	spy := &relayRepoSpy{Repository: repo}
	spy.setRetryErr(errors.New("event outbox claim lost"))
	reader := newRelayBoundReader()
	reader.setBound("default", "default")
	relay := NewEventOutboxRelay(spy, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
	})
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-claim-lost", "token-claim-lost")
	relay.deliverFact(fact)
	records := spy.retryRecords()
	if len(records) != 1 {
		t.Fatalf("claim loss rewrote %d times, want exactly 1", len(records))
	}
	if !strings.Contains(records[0].LastError, "Space-bound") {
		t.Fatalf("policy reason = %q, want Space policy reason", records[0].LastError)
	}
	state := readRelayFactState(t, dsn, obj.ID, repository.EventTypeFileNotify11)
	requireRelayFactNotDelivered(t, "claim loss", state)
	if state.Status != "inflight" {
		t.Fatalf("claim loss status = %s, want inflight", state.Status)
	}
}
