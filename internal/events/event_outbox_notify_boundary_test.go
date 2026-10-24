package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestOutboxNotifyBoundary_UnboundDeliversVerbatim(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "notify-unbound.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "boundary/notify-unbound.txt")
	payload := fixedNotifyPayload(obj)
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, payload)
	target := &captureTarget{}
	relay, srv := newRelay(repo, target, EventOutboxRelayOptions{MaxAttempts: 3})
	defer srv.Close()
	setDeleteRule(t, repo, srv.URL)
	reader := newRelayBoundReader()
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-unbound", "token-unbound")
	relay.deliverFact(fact)
	waitForTarget(t, target, 1, 5*time.Second)

	bodies := target.bodiesCopy()
	if len(bodies) != 1 || string(bodies[0]) != string(payload) {
		t.Fatalf("unbound delivery changed payload bytes: posts=%d body=%s", len(bodies), bodies)
	}
	state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileNotify11)
	if state.Status != "delivered" || state.DeliveredRows != 1 || state.DeliveredAtNS == 0 {
		t.Fatalf("unbound fact not delivered: %+v", state)
	}
	if reader.callCount() < 2 {
		t.Fatalf("boundary observations = %d, want initial + per-target", reader.callCount())
	}
}

func TestOutboxNotifyBoundary_BoundAndInactiveFailAsPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*relayBoundReader)
		reason string
	}{
		{name: "bound", setup: func(r *relayBoundReader) { r.setBound("default", "default") },
			reason: "Space-bound resource"},
		{name: "inactive tenant", setup: func(r *relayBoundReader) { r.setInactive("default", "default") },
			reason: "tenant unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := "file:" + filepath.Join(t.TempDir(), "notify-policy-"+strings.ReplaceAll(tc.name, " ", "-")+".db")
			repo := openRelayTestRepoAt(t, dsn)
			obj := seedRelayObject(t, repo, "boundary/notify-policy.txt")
			seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
			target := &captureTarget{}
			relay, srv := newRelay(repo, target, EventOutboxRelayOptions{MaxAttempts: 3})
			defer srv.Close()
			setDeleteRule(t, repo, srv.URL)
			reader := newRelayBoundReader()
			tc.setup(reader)
			relay.WithResourceBoundary(reader)

			fact := claimSingleRelayFact(t, repo, "owner-policy", "token-policy")
			relay.deliverFact(fact)
			time.Sleep(100 * time.Millisecond)
			if got := target.count(); got != 0 {
				t.Fatalf("policy refusal produced %d POSTs, want 0", got)
			}
			state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileNotify11)
			requireRelayFactFailed(t, tc.name, state)
			if !strings.Contains(state.LastError, tc.reason) {
				t.Fatalf("last_error = %q, want policy reason %q", state.LastError, tc.reason)
			}
			if strings.Contains(state.LastError, ErrSinkUnauthorized.Error()) {
				t.Fatalf("notification policy reused the L2 rejection reason: %q", state.LastError)
			}
			if pending, err := repo.ClaimEventOutbox(context.Background(), "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
				t.Fatalf("policy-failed fact reclaimable: len=%d err=%v", len(pending), err)
			}
		})
	}
}

func TestOutboxNotifyBoundary_ContradictoryRecordsUseBoundedRetry(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "payload tenant differs from row", mutate: func(doc map[string]any) {
			doc["tenant"] = "other"
		}},
		{name: "event type differs", mutate: func(doc map[string]any) {
			doc["event_type"] = string(repository.EventTypeFileDeleted11)
		}},
		{name: "record principal differs", mutate: func(doc map[string]any) {
			record := firstOutboxNotifyRecord(doc)
			record["userIdentity"].(map[string]any)["principalId"] = "other"
		}},
		{name: "record bucket differs", mutate: func(doc map[string]any) {
			record := firstOutboxNotifyRecord(doc)
			s3 := record["s3"].(map[string]any)
			s3["bucket"].(map[string]any)["name"] = "other"
		}},
		{name: "record object key differs", mutate: func(doc map[string]any) {
			record := firstOutboxNotifyRecord(doc)
			s3 := record["s3"].(map[string]any)
			s3["object"].(map[string]any)["key"] = "other"
		}},
		{name: "records missing", mutate: func(doc map[string]any) {
			doc["records"] = []any{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := "file:" + filepath.Join(t.TempDir(), "notify-contradiction.db")
			repo := openRelayTestRepoAt(t, dsn)
			obj := seedRelayObject(t, repo, "boundary/notify-contradiction.txt")
			payload := mutateOutboxNotifyPayload(t, fixedNotifyPayload(obj), tc.mutate)
			seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, payload)
			target := &captureTarget{}
			relay, srv := newRelay(repo, target, EventOutboxRelayOptions{MaxAttempts: 3})
			defer srv.Close()
			setDeleteRule(t, repo, srv.URL)
			reader := newRelayBoundReader()
			relay.WithResourceBoundary(reader)

			fact := claimSingleRelayFact(t, repo, "owner-invalid", "token-invalid")
			relay.deliverFact(fact)
			time.Sleep(100 * time.Millisecond)
			if got := target.count(); got != 0 {
				t.Fatalf("invalid notify fact produced %d POSTs, want 0", got)
			}
			state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileNotify11)
			requireRelayFactPending(t, tc.name, state)
			if state.Attempts != 1 {
				t.Fatalf("invalid notify attempts = %d, want 1", state.Attempts)
			}
		})
	}
}

func TestOutboxNotifyBoundary_RechecksBeforeEachTarget(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "notify-two-targets.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "boundary/notify-two-targets.txt")
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
	first := &captureTarget{}
	second := &captureTarget{}
	firstSrv := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstSrv.Close()
	secondSrv := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondSrv.Close()
	rules := []repository.NotificationRule{
		{ID: "rule-1", Events: []string{"s3:ObjectRemoved:Delete"}, EndpointURL: firstSrv.URL},
		{ID: "rule-2", Events: []string{"s3:ObjectRemoved:Delete"}, EndpointURL: secondSrv.URL},
	}
	if err := repo.SetBucketNotifications(context.Background(), obj.TenantID, obj.Bucket, rules); err != nil {
		t.Fatalf("set notifications: %v", err)
	}
	reader := newRelayBoundReader()
	reader.setOnCall(func(call int) {
		if call == 3 {
			reader.setBound("default", "default")
		}
	})
	relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
	})
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-two-targets", "token-two-targets")
	relay.deliverFact(fact)
	waitForTarget(t, first, 1, 5*time.Second)
	time.Sleep(100 * time.Millisecond)
	if got := second.count(); got != 0 {
		t.Fatalf("second target received %d POSTs after boundary changed, want 0", got)
	}
	state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileNotify11)
	requireRelayFactFailed(t, "boundary changed before second target", state)
	if !strings.Contains(state.LastError, "Space-bound resource") {
		t.Fatalf("last_error = %q, want Space policy reason", state.LastError)
	}
	if reader.callCount() != 3 {
		t.Fatalf("boundary observations = %d, want initial + two targets", reader.callCount())
	}
}

func TestOutboxNotifyBoundary_NoMatchCompletesOnlyAfterAdmission(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "notify-no-match.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "boundary/notify-no-match.txt")
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileNotify11, fixedNotifyPayload(obj))
	reader := newRelayBoundReader()
	relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
	})
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-no-match", "token-no-match")
	relay.deliverFact(fact)
	state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileNotify11)
	if state.Status != "delivered" || state.DeliveredRows != 1 {
		t.Fatalf("no-match fact not completed after admission: %+v", state)
	}
	if reader.callCount() != 1 {
		t.Fatalf("no-match boundary observations = %d, want 1 before completion", reader.callCount())
	}
}

func mutateOutboxNotifyPayload(t *testing.T, payload []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode notify payload: %v", err)
	}
	mutate(document)
	mutated, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode mutated notify payload: %v", err)
	}
	return mutated
}

func firstOutboxNotifyRecord(document map[string]any) map[string]any {
	records, _ := document["records"].([]any)
	return records[0].(map[string]any)
}
