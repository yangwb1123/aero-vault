package events

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

const relayBoundarySpaceID = "12345678-1234-1234-1234-123456789abc"

// relayFactState is an exact fact-ID read used by the boundary tests. It never
// mutates status; the relay (or its real repository) must produce every
// transition.
type relayFactState struct {
	ID            int64
	EventType     string
	Status        string
	Attempts      int
	LastError     string
	DeliveredAtNS int64
	DeliveredRows int64
}

func readRelayFactState(t *testing.T, dsn string, originID int64, eventType repository.OutboxEventType) relayFactState {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open relay fact state: %v", err)
	}
	defer db.Close()
	var state relayFactState
	err = db.QueryRowContext(context.Background(), `SELECT id,event_type,status,attempts,
COALESCE(last_error,''),delivered_at_ns
FROM event_outbox WHERE origin_id=$1 AND event_type=$2 ORDER BY id DESC LIMIT 1`,
		originID, string(eventType)).
		Scan(&state.ID, &state.EventType, &state.Status, &state.Attempts, &state.LastError, &state.DeliveredAtNS)
	if err != nil {
		t.Fatalf("read relay fact state: %v", err)
	}
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM event_outbox_delivered WHERE outbox_id=$1`, state.ID).
		Scan(&state.DeliveredRows); err != nil {
		t.Fatalf("read relay delivered record: %v", err)
	}
	return state
}

func seedRelayFactForObject(
	t *testing.T, repo repository.Repository, obj repository.Object,
	eventType repository.OutboxEventType, payload []byte,
) {
	t.Helper()
	if err := repo.HardDeleteObjectWithEvent(context.Background(), obj.TenantID, obj.Bucket, obj.Key,
		repository.AuditEntry{}, []repository.OutboxFact{{
			EventType: eventType, OriginID: obj.ID, TenantID: obj.TenantID, Payload: payload,
		}}); err != nil {
		t.Fatalf("seed %s fact: %v", eventType, err)
	}
}

func claimSingleRelayFact(
	t *testing.T, repo repository.Repository, owner, token string,
) repository.EventOutboxRow {
	t.Helper()
	facts, err := repo.ClaimEventOutbox(context.Background(), owner, token, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim relay fact: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("claimed facts = %d, want exactly 1", len(facts))
	}
	return facts[0]
}

// relayBoundReader is a mutable BindingReader double. The relay must treat its
// observations as authoritative and re-observe before every target.
type relayBoundReader struct {
	mu      sync.Mutex
	results map[string]spaces.BindingResult
	err     error
	calls   []string
	onCall  func(int)
}

func newRelayBoundReader() *relayBoundReader {
	return &relayBoundReader{results: make(map[string]spaces.BindingResult)}
}

func relayBoundaryKey(tenant, bucket string) string {
	return tenant + "\x00" + bucket
}

func relayUnboundResult(tenant, bucket string) spaces.BindingResult {
	return spaces.BindingResult{TenantID: tenant, Bucket: bucket, State: spaces.BindingUnbound}
}

func relayBoundResult(tenant, bucket string) spaces.BindingResult {
	return spaces.BindingResult{TenantID: tenant, Bucket: bucket, TenantKnown: true,
		TenantStatus: "active", State: spaces.BindingBound,
		SpaceID: relayBoundarySpaceID, Revision: 1}
}

func (r *relayBoundReader) setResult(tenant, bucket string, result spaces.BindingResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[relayBoundaryKey(tenant, bucket)] = result
}

func (r *relayBoundReader) setBound(tenant, bucket string) {
	r.setResult(tenant, bucket, relayBoundResult(tenant, bucket))
}

func (r *relayBoundReader) setInactive(tenant, bucket string) {
	r.setResult(tenant, bucket, spaces.BindingResult{TenantID: tenant, Bucket: bucket,
		TenantKnown: true, TenantStatus: "disabled", State: spaces.BindingUnbound})
}

func (r *relayBoundReader) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *relayBoundReader) setOnCall(fn func(int)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onCall = fn
}

func (r *relayBoundReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *relayBoundReader) ProbeSpaceBinding(
	ctx context.Context, tenant, bucket string,
) (spaces.BindingResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, relayBoundaryKey(tenant, bucket))
	call := len(r.calls)
	onCall := r.onCall
	r.mu.Unlock()
	if onCall != nil {
		onCall(call)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return spaces.BindingResult{}, r.err
	}
	if result, ok := r.results[relayBoundaryKey(tenant, bucket)]; ok {
		return result, nil
	}
	return relayUnboundResult(tenant, bucket), nil
}

// relayRetryRecord captures the exact fencing arguments forwarded by the relay.
type relayRetryRecord struct {
	ID          int64
	Owner       string
	Token       string
	LastError   string
	Next        time.Time
	MaxAttempts int
}

type relayCompleteRecord struct {
	ID    int64
	Owner string
	Token string
}

// relayRepoSpy embeds the real repository so every untouched operation still
// uses the disk-backed SQLite fixture. Only the fenced writes under test are
// intercepted.
type relayRepoSpy struct {
	repository.Repository

	mu          sync.Mutex
	retries     []relayRetryRecord
	completes   []relayCompleteRecord
	retryErr    error
	completeErr error
}

func (s *relayRepoSpy) RetryEventOutbox(
	ctx context.Context, id int64, owner, token, lastErr string, next time.Time, maxAttempts int,
) error {
	s.mu.Lock()
	s.retries = append(s.retries, relayRetryRecord{ID: id, Owner: owner, Token: token,
		LastError: lastErr, Next: next, MaxAttempts: maxAttempts})
	err := s.retryErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Repository.RetryEventOutbox(ctx, id, owner, token, lastErr, next, maxAttempts)
}

func (s *relayRepoSpy) CompleteEventOutbox(ctx context.Context, id int64, owner, token string) error {
	s.mu.Lock()
	s.completes = append(s.completes, relayCompleteRecord{ID: id, Owner: owner, Token: token})
	err := s.completeErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Repository.CompleteEventOutbox(ctx, id, owner, token)
}

func (s *relayRepoSpy) setRetryErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryErr = err
}

func (s *relayRepoSpy) retryRecords() []relayRetryRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]relayRetryRecord(nil), s.retries...)
}

func (s *relayRepoSpy) completeRecords() []relayCompleteRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]relayCompleteRecord(nil), s.completes...)
}

func requireRelayFactNotDelivered(t *testing.T, label string, state relayFactState) {
	t.Helper()
	if state.Status == "delivered" || state.DeliveredAtNS != 0 || state.DeliveredRows != 0 {
		t.Fatalf("%s: fact must not be delivered, got status=%s delivered_at_ns=%d delivered_rows=%d",
			label, state.Status, state.DeliveredAtNS, state.DeliveredRows)
	}
}

func requireRelayFactPending(t *testing.T, label string, state relayFactState) {
	t.Helper()
	requireRelayFactNotDelivered(t, label, state)
	if state.Status != "pending" || state.Attempts < 1 {
		t.Fatalf("%s: want pending with attempts >= 1, got status=%s attempts=%d",
			label, state.Status, state.Attempts)
	}
	if state.LastError == "" {
		t.Fatalf("%s: pending retry has no persisted last_error", label)
	}
}

func requireRelayFactFailed(t *testing.T, label string, state relayFactState) {
	t.Helper()
	requireRelayFactNotDelivered(t, label, state)
	if state.Status != "failed" {
		t.Fatalf("%s: want terminal failed, got status=%s attempts=%d last_error=%q",
			label, state.Status, state.Attempts, state.LastError)
	}
	if state.LastError == "" {
		t.Fatalf("%s: terminal failure has no persisted last_error", label)
	}
}
