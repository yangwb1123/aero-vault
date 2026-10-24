package events

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestOutboxDeletedAuthority_MalformedFactsNeverReachL2(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "payload tenant differs from row", mutate: func(doc map[string]any) {
			doc["tenant"] = "other"
		}},
		{name: "event type differs", mutate: func(doc map[string]any) {
			doc["event_type"] = string(repository.EventTypeFileNotify11)
		}},
		{name: "bucket empty", mutate: func(doc map[string]any) {
			doc["bucket"] = ""
		}},
		{name: "key empty", mutate: func(doc map[string]any) {
			doc["key"] = ""
		}},
		{name: "object id zero", mutate: func(doc map[string]any) {
			doc["object_id"] = 0
		}},
		{name: "object id mismatches origin", mutate: func(doc map[string]any) {
			doc["object_id"] = 999999
		}},
		{name: "object id wrong type", mutate: func(doc map[string]any) {
			doc["object_id"] = "not-a-number"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := "file:" + filepath.Join(t.TempDir(), "deleted-invalid.db")
			repo := openRelayTestRepoAt(t, dsn)
			obj := seedRelayObject(t, repo, "deleted/invalid.txt")
			payload := mutateOutboxDeletedPayload(t, fixedDeletedPayload(obj), tc.mutate)
			seedRelayFactForObject(t, repo, obj, repository.EventTypeFileDeleted11, payload)
			target := &l2SinkTarget{echo: true}
			relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{})
			defer srv.Close()
			reader := newRelayBoundReader()
			reader.setBound("default", "default")
			relay.WithResourceBoundary(reader)

			fact := claimSingleRelayFact(t, repo, "owner-deleted-invalid", "token-deleted-invalid")
			relay.deliverFact(fact)
			time.Sleep(100 * time.Millisecond)
			if got := target.count(); got != 0 {
				t.Fatalf("malformed deleted fact produced %d L2 POSTs, want 0", got)
			}
			if got := reader.callCount(); got != 0 {
				t.Fatalf("deleted fact consulted Space binding %d times, want 0", got)
			}
			state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileDeleted11)
			requireRelayFactPending(t, tc.name, state)
			if state.Attempts != 1 {
				t.Fatalf("malformed deleted attempts = %d, want 1", state.Attempts)
			}
		})
	}
}

func TestOutboxDeletedAuthority_BoundSpaceStillDeliversVerbatim(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "deleted-bound.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "deleted/bound.txt")
	payload := fixedDeletedPayload(obj)
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileDeleted11, payload)
	target := &l2SinkTarget{echo: true}
	relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{})
	defer srv.Close()
	reader := newRelayBoundReader()
	reader.setBound("default", "default")
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-deleted-bound", "token-deleted-bound")
	relay.deliverFact(fact)
	waitForTarget(t, target, 1, 5*time.Second)

	bodies, factIDs, tokens := target.snapshot()
	if len(bodies) != 1 || string(bodies[0]) != string(payload) {
		t.Fatalf("Space-bound deleted payload changed bytes: posts=%d body=%s", len(bodies), bodies)
	}
	if tokens[0] != "Bearer "+l2TestToken {
		t.Fatalf("L2 token = %q, want bound bearer token", tokens[0])
	}
	if factIDs[0] == "" {
		t.Fatal("L2 receipt lost the outbox fact id")
	}
	state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileDeleted11)
	if state.Status != "delivered" || state.DeliveredRows != 1 {
		t.Fatalf("bound deleted fact not delivered to L2: %+v", state)
	}
	if got := reader.callCount(); got != 0 {
		t.Fatalf("deleted/L2 path consulted Space binding %d times, want 0", got)
	}
}

func TestOutboxDeletedAuthority_LegacyOmissionStillDelivers(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "deleted-legacy.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "deleted/legacy.txt")
	const legacyDeleted = `{"schema_version":"1.1","event_type":"vault.file.deleted@1.1","tenant":"default","bucket":"default","key":"deleted/legacy.txt","version_id":"v-1","size":7,"etag":"etag-1","backend":"local","request_id":"req-old","actor":"alice"}`
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileDeleted11, []byte(legacyDeleted))
	target := &l2SinkTarget{echo: true}
	relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{})
	defer srv.Close()
	reader := newRelayBoundReader()
	reader.setBound("default", "default")
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-deleted-legacy", "token-deleted-legacy")
	relay.deliverFact(fact)
	waitForTarget(t, target, 1, 5*time.Second)
	bodies, _, _ := target.snapshot()
	if len(bodies) != 1 || string(bodies[0]) != legacyDeleted {
		t.Fatalf("legacy deleted fixture changed bytes: posts=%d body=%s", len(bodies), bodies)
	}
	state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileDeleted11)
	if state.Status != "delivered" || state.DeliveredRows != 1 {
		t.Fatalf("legacy deleted fact not delivered: %+v", state)
	}
}

func TestOutboxDeletedAuthority_UnboundSinkMappingUnchanged(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "deleted-unbound-sink.db")
	repo := openRelayTestRepoAt(t, dsn)
	obj := seedRelayObject(t, repo, "deleted/unbound-sink.txt")
	seedRelayFactForObject(t, repo, obj, repository.EventTypeFileDeleted11, fixedDeletedPayload(obj))
	target := &l2SinkTarget{echo: true}
	relay, srv := newL2Relay(repo, target, map[string]string{"other-tenant": l2TestToken}, EventOutboxRelayOptions{})
	defer srv.Close()
	reader := newRelayBoundReader()
	reader.setBound("default", "default")
	relay.WithResourceBoundary(reader)

	fact := claimSingleRelayFact(t, repo, "owner-deleted-unbound", "token-deleted-unbound")
	relay.deliverFact(fact)
	time.Sleep(100 * time.Millisecond)
	if got := target.count(); got != 0 {
		t.Fatalf("unbound L2 tenant produced %d POSTs, want 0", got)
	}
	state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileDeleted11)
	if state.Status != "delivered" || state.DeliveredRows != 1 {
		t.Fatalf("ErrSinkNotBound mapping changed under Space bound: %+v", state)
	}
	if strings.TrimSpace(state.LastError) != "" {
		t.Fatalf("unbound L2 completion persisted an error: %q", state.LastError)
	}
}

func TestOutboxDeletedAuthority_InvalidScopeNeverReachesL2(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tenant string
		bucket string
	}{
		{name: "tenant wildcard", tenant: "*", bucket: "default"},
		{name: "tenant control character", tenant: "bad\n", bucket: "default"},
		{name: "bucket wildcard", tenant: "default", bucket: "*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := "file:" + filepath.Join(t.TempDir(), "deleted-invalid-scope.db")
			repo := openRelayTestRepoAt(t, dsn)
			obj := seedRelayObject(t, repo, "deleted/invalid-scope.txt")
			payload := mutateOutboxDeletedPayload(t, fixedDeletedPayload(obj), func(doc map[string]any) {
				doc["tenant"], doc["bucket"] = tc.tenant, tc.bucket
			})
			if err := repo.HardDeleteObjectWithEvent(context.Background(), obj.TenantID, obj.Bucket, obj.Key,
				repository.AuditEntry{}, []repository.OutboxFact{{
					EventType: repository.EventTypeFileDeleted11, OriginID: obj.ID,
					TenantID: tc.tenant, Payload: payload,
				}}); err != nil {
				t.Fatalf("seed invalid-scope deleted fact: %v", err)
			}
			target := &l2SinkTarget{echo: true}
			relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{})
			defer srv.Close()

			fact := claimSingleRelayFact(t, repo, "owner-deleted-scope", "token-deleted-scope")
			relay.deliverFact(fact)
			time.Sleep(100 * time.Millisecond)
			if got := target.count(); got != 0 {
				t.Fatalf("invalid deleted scope produced %d L2 POSTs, want 0", got)
			}
			state := readRelayFactState(t, dsn, fact.OriginID, repository.EventTypeFileDeleted11)
			requireRelayFactPending(t, tc.name, state)
			if state.Attempts != 1 {
				t.Fatalf("invalid deleted scope attempts = %d, want 1", state.Attempts)
			}
		})
	}
}

func mutateOutboxDeletedPayload(t *testing.T, payload []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode deleted payload: %v", err)
	}
	mutate(document)
	mutated, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode mutated deleted payload: %v", err)
	}
	return mutated
}
