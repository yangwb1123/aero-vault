package events

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// captureTarget records every POST body it receives (mutex-protected — the
// relay delivers per-fact in goroutines).
type captureTarget struct {
	mu     sync.Mutex
	bodies [][]byte
	status int // HTTP status to return
	delay  time.Duration
}

func (c *captureTarget) handler(w http.ResponseWriter, r *http.Request) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	body := make([]byte, 0, 64)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, body)
	c.mu.Unlock()
	if c.status == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(c.status)
}

func (c *captureTarget) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *captureTarget) bodiesCopy() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.bodies...)
}

func openRelayTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	return openRelayTestRepoAt(t, "file:"+filepath.Join(t.TempDir(), "relay.db"))
}

// openRelayTestRepoAt opens a relay repo at an explicit DSN so tests can open
// a second raw SQL connection to the same file (backdate timestamps for
// prune-horizon tests).
func openRelayTestRepoAt(t *testing.T, dsn string) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

// backdateEventOutboxAt backdates one timestamp column on every event_outbox
// row for the given origin, via a raw SQLite connection to the same file
// (mirrors backdateEventOutboxDelivered in package repository — the
// Repository interface exposes no timestamp mutation). column must be a
// fixed constant.
func backdateEventOutboxAt(t *testing.T, dsn, column string, originID int64, when time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE event_outbox SET `+column+`=$1 WHERE origin_id=$2`, when.UTC().UnixNano(), originID); err != nil {
		t.Fatalf("backdate %s: %v", column, err)
	}
}

func seedRelayObject(t *testing.T, repo repository.Repository, key string) repository.Object {
	t.Helper()
	obj, err := repo.UpsertObject(context.Background(), repository.Object{
		TenantID: "default", Bucket: "default", Key: key, VersionID: "v-1",
		Backend: "local", StorageKey: "default/default/" + key + "@v-1",
		Size: 7, ETag: "etag-1", ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("seed object: %v", err)
	}
	return obj
}

// fixedNotifyPayload is a notify@1.1 fact with a pinned sequencer so the
// byte-exactness of relay delivery is assertable.
func fixedNotifyPayload(obj repository.Object) []byte {
	return BuildNotifyFact(obj, "alice", "req-1", "default", "8f3a2c9e4b1d7f06a5c0e2d94b7a1f3e")
}

func fixedDeletedPayload(obj repository.Object) []byte {
	return BuildDeletedFact(obj, "alice", "req-1", "default")
}

func seedRelayFacts(t *testing.T, repo repository.Repository, obj repository.Object) {
	t.Helper()
	err := repo.HardDeleteObjectWithEvent(context.Background(), "default", "default", obj.Key, repository.AuditEntry{}, []repository.OutboxFact{
		{EventType: repository.EventTypeFileDeleted11, OriginID: obj.ID, TenantID: "default", Payload: fixedDeletedPayload(obj)},
		{EventType: repository.EventTypeFileNotify11, OriginID: obj.ID, TenantID: "default", Payload: fixedNotifyPayload(obj)},
	})
	if err != nil {
		t.Fatalf("seed facts: %v", err)
	}
}

func setDeleteRule(t *testing.T, repo repository.Repository, url string) {
	t.Helper()
	if err := repo.SetBucketNotifications(context.Background(), "default", "default", []repository.NotificationRule{{
		ID:          "rule-1",
		Events:      []string{"s3:ObjectRemoved:Delete"},
		EndpointURL: url,
	}}); err != nil {
		t.Fatalf("set notifications: %v", err)
	}
}

func newRelay(repo repository.Repository, target *captureTarget, opts EventOutboxRelayOptions) (*EventOutboxRelay, *httptest.Server) {
	srv := httptest.NewServer(http.HandlerFunc(target.handler))
	opts.HTTPTimeout = 5 * time.Second
	return NewEventOutboxRelay(repo, nil, opts), srv
}

// targetCounter is implemented by both captureTarget and l2SinkTarget.
type targetCounter interface {
	count() int
}

func waitForTarget(t *testing.T, target targetCounter, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if target.count() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("target received %d POSTs, want %d", target.count(), want)
}

// ── AC-2(1/4): claim → deliver(byte-exact) → complete; exactly-once ─────────

func TestOutboxRelay_DeliveryLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := openRelayTestRepo(t)
	obj := seedRelayObject(t, repo, "docs/a.txt")
	seedRelayFacts(t, repo, obj)
	target := &captureTarget{}
	relay, srv := newRelay(repo, target, EventOutboxRelayOptions{PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3})
	defer srv.Close()
	setDeleteRule(t, repo, srv.URL)

	relay.deliverBatch(ctx)
	waitForTarget(t, target, 1, 5*time.Second)

	// Exactly one POST, byte-exact to the stored notify@1.1 payload.
	bodies := target.bodiesCopy()
	if len(bodies) != 1 {
		t.Fatalf("POSTs = %d, want 1", len(bodies))
	}
	if string(bodies[0]) != string(fixedNotifyPayload(obj)) {
		t.Errorf("relay did not deliver the payload byte-exact\n got: %s\nwant: %s", bodies[0], fixedNotifyPayload(obj))
	}

	// Both facts completed: nothing due remains (exactly-once post-complete).
	if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
		t.Fatalf("delivered facts reclaimed: len=%d err=%v", len(pending), err)
	}

	// A second batch must not re-deliver.
	relay.deliverBatch(ctx)
	time.Sleep(50 * time.Millisecond)
	if got := target.count(); got != 1 {
		t.Fatalf("POSTs after second batch = %d, want 1", got)
	}
}

// ── AC-2(2): 5xx → backoff retry → eventual delivery ────────────────────────

func TestOutboxRelay_RetriesOn5xx(t *testing.T) {
	ctx := context.Background()
	repo := openRelayTestRepo(t)
	obj := seedRelayObject(t, repo, "retry.txt")
	seedRelayFacts(t, repo, obj)
	target := &captureTarget{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First delivery fails with a real 500; subsequent ones succeed.
		// (Writing the status through captureTarget — a second WriteHeader
		// after target.handler's 200 would be superfluous and the client
		// would see 200, defeating the retry path under test.)
		target.status = http.StatusInternalServerError
		if target.count() > 0 {
			target.status = 0
		}
		target.handler(w, r)
	}))
	defer srv.Close()
	setDeleteRule(t, repo, srv.URL)
	relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
	})

	relay.deliverBatch(ctx)
	waitForTarget(t, target, 1, 5*time.Second)

	// The 500 response schedules a retry: the fact is not due immediately.
	if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
		t.Fatalf("failed delivery immediately reclaimable: len=%d err=%v", len(pending), err)
	}

	// After the backoff elapses (attempt=1 → base 1s ±25%), the next batch
	// reclaims and delivers; the second POST succeeds.
	time.Sleep(1300 * time.Millisecond)
	relay.deliverBatch(ctx)
	waitForTarget(t, target, 2, 5*time.Second)
	bodies := target.bodiesCopy()
	if string(bodies[1]) != string(fixedNotifyPayload(obj)) {
		t.Errorf("retried delivery payload mismatch")
	}
	if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token-2", 10, time.Minute); err != nil || len(pending) != 0 {
		t.Fatalf("facts not completed after retry: len=%d err=%v", len(pending), err)
	}
}

// ── D7: claim-lost (lease expired mid-flight) is not retried in-loop; the
// lease-expired fact becomes reclaimable (at-least-once redelivery) ──────────

func TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule(t *testing.T) {
	ctx := context.Background()
	repo := openRelayTestRepo(t)
	obj := seedRelayObject(t, repo, "slow.txt")
	seedRelayFacts(t, repo, obj)
	target := &captureTarget{delay: 100 * time.Millisecond} // POST outlives the lease
	srv := httptest.NewServer(http.HandlerFunc(target.handler))
	defer srv.Close()
	setDeleteRule(t, repo, srv.URL)

	// 50ms lease: the 100ms POST crosses the lease boundary, so the relay's
	// complete fails with claim-lost — it must warn and NOT retry in-loop.
	relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 50 * time.Millisecond, MaxAttempts: 3,
	})
	relay.deliverBatch(ctx)
	waitForTarget(t, target, 1, 5*time.Second)

	// No in-loop retry: claim-lost must not reschedule (no backoff 'pending'
	// state — D7). The fact stays 'inflight' with an expired lease, so the
	// next batch reclaims it immediately — an at-least-once re-POST is the
	// designed lease-reclaim recovery, not a double-schedule.
	relay.deliverBatch(ctx)
	waitForTarget(t, target, 2, 5*time.Second)

	// The expired-lease fact is reclaimable by another owner (crash
	// redelivery, at-least-once): the next delivery repeats the POST.
	reclaimed, err := repo.ClaimEventOutbox(ctx, "relay-2", "token", 10, 5*time.Second)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) == 0 {
		t.Fatal("lease-expired facts not reclaimable")
	}
	for _, fact := range reclaimed {
		if err := repo.CompleteEventOutbox(ctx, fact.ID, "relay-2", "token"); err != nil {
			t.Fatalf("complete reclaimed fact: %v", err)
		}
	}
}

// ── D3: deleted@1.1 completes silently — no POST, no local replay ───────────

func TestOutboxRelay_DeletedFactCompletesWithoutDelivery(t *testing.T) {
	ctx := context.Background()
	repo := openRelayTestRepo(t)
	obj := seedRelayObject(t, repo, "silent.txt")
	if err := repo.HardDeleteObjectWithEvent(ctx, "default", "default", obj.Key, repository.AuditEntry{}, []repository.OutboxFact{
		{EventType: repository.EventTypeFileDeleted11, OriginID: obj.ID, TenantID: "default", Payload: fixedDeletedPayload(obj)},
	}); err != nil {
		t.Fatal(err)
	}
	target := &captureTarget{}
	relay, srv := newRelay(repo, target, EventOutboxRelayOptions{PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3})
	defer srv.Close()
	setDeleteRule(t, repo, srv.URL)

	relay.deliverBatch(ctx)
	time.Sleep(100 * time.Millisecond)
	if got := target.count(); got != 0 {
		t.Fatalf("deleted@1.1 triggered %d POSTs, want 0", got)
	}
	if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
		t.Fatalf("deleted@1.1 not completed: len=%d err=%v", len(pending), err)
	}
}

// ── backoff bounds (billingBackoff shape: 1s base, 2×, 5min cap; jitter is
// downward-only [0.75, 1.0)×base — webhook.go jitter, not ±25%, D-7) ────────

func TestEventOutboxBackoffBounds(t *testing.T) {
	expect := []struct {
		attempt    int
		baseMillis int64
	}{
		{1, 1000},
		{2, 2000},
		{3, 4000},
		{4, 8000},
		{9, 256000},  // 2^8 = 256s < 5min cap → not capped
		{20, 300000}, // 2^19 way past the cap → base capped
	}
	for _, tc := range expect {
		delay := eventOutboxBackoff(tc.attempt)
		// jitter(d) ∈ [0.75, 1.0)×d: n ∈ [0, d/2) → d - d/4 + n/2 ∈ [0.75d, d).
		min, max := tc.baseMillis*3/4, tc.baseMillis-1
		if delay.Milliseconds() < min || delay.Milliseconds() > max {
			t.Errorf("attempt %d: delay = %v, want within [%dms, %dms) (downward-only jitter of %dms)",
				tc.attempt, delay, min, max, tc.baseMillis)
		}
	}
	// attempt 0 is normalized to 1.
	if d := eventOutboxBackoff(0); d <= 0 {
		t.Errorf("attempt 0 delay = %v", d)
	}
}

var _ = fmt.Sprintf // keep fmt import for future diagnostics

// ── L2 AuditSink tests (AC-2, H2) ───────────────────────────────────────────

// l2SinkTarget is the L2 audit-sink httptest target: it records every POST
// body and the X-Audit-Fact-Id / Authorization headers, optionally echoes the
// fact id (echo receipt, D5), returns a fixed status, and can delay or block
// the response (AC-3-style). Mutex-protected: the relay delivers per-fact in
// goroutines.
type l2SinkTarget struct {
	mu      sync.Mutex
	bodies  [][]byte
	factIDs []string
	tokens  []string
	status  int // HTTP status to return (0 → 200)
	echo    bool
	delay   time.Duration
	block   <-chan struct{} // non-nil → never respond until closed
}

func (l *l2SinkTarget) handler(w http.ResponseWriter, r *http.Request) {
	if l.delay > 0 {
		time.Sleep(l.delay)
	}
	if l.block != nil {
		<-l.block
	}
	body := make([]byte, 0, 64)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	l.mu.Lock()
	l.bodies = append(l.bodies, body)
	l.factIDs = append(l.factIDs, r.Header.Get(auditSinkL2FactIDHeader))
	l.tokens = append(l.tokens, r.Header.Get("Authorization"))
	status := l.status
	echo := l.echo
	l.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if echo {
		w.Header().Set(auditSinkL2FactIDHeader, r.Header.Get(auditSinkL2FactIDHeader))
	}
	w.WriteHeader(status)
}

func (l *l2SinkTarget) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

func (l *l2SinkTarget) snapshot() (bodies [][]byte, factIDs, tokens []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]byte(nil), l.bodies...), append([]string(nil), l.factIDs...), append([]string(nil), l.tokens...)
}

// l2TestToken is the fixture bearer token (≥16 chars per config hygiene).
const l2TestToken = "l2-test-token-0123456789"

// newL2Relay builds a relay whose AuditSink is a real L2 adapter pointed at
// the given httptest target, with the given tenant→token bindings.
func newL2Relay(repo repository.Repository, target *l2SinkTarget, bindings map[string]string, opts EventOutboxRelayOptions) (*EventOutboxRelay, *httptest.Server) {
	srv := httptest.NewServer(http.HandlerFunc(target.handler))
	sink, err := NewAuditSinkL2(srv.URL, bindings, &http.Client{Timeout: 5 * time.Second}, nil)
	if err != nil {
		panic(err)
	}
	opts.AuditSink = sink
	if opts.HTTPTimeout == 0 {
		opts.HTTPTimeout = 5 * time.Second
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Hour
	}
	if opts.BatchSize == 0 {
		opts.BatchSize = 32
	}
	if opts.ClaimTTL == 0 {
		opts.ClaimTTL = 30 * time.Second
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 3
	}
	return NewEventOutboxRelay(repo, nil, opts), srv
}

func seedDeletedFact(t *testing.T, repo repository.Repository, obj repository.Object, payload []byte) {
	t.Helper()
	if err := repo.HardDeleteObjectWithEvent(context.Background(), "default", "default", obj.Key,
		repository.AuditEntry{}, []repository.OutboxFact{
			{EventType: repository.EventTypeFileDeleted11, OriginID: obj.ID, TenantID: "default", Payload: payload},
		}); err != nil {
		t.Fatalf("seed deleted fact: %v", err)
	}
}

// ── AC-2: claim → L2 publish (verbatim + echo) → complete ───────────────────

func TestOutboxRelay_DeliversDeletedFactToL2(t *testing.T) {
	ctx := context.Background()

	t.Run("bound tenant: verbatim POST with bearer + fact-id header, echo → delivered", func(t *testing.T) {
		repo := openRelayTestRepo(t)
		obj := seedRelayObject(t, repo, "l2/a.txt")
		payload := fixedDeletedPayload(obj) // includes object_id (AC-4)
		seedDeletedFact(t, repo, obj, payload)
		target := &l2SinkTarget{echo: true}
		relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{})
		defer srv.Close()

		relay.deliverBatch(ctx)
		waitForTarget(t, target, 1, 5*time.Second)

		bodies, factIDs, tokens := target.snapshot()
		if len(bodies) != 1 {
			t.Fatalf("POSTs = %d, want 1", len(bodies))
		}
		if string(bodies[0]) != string(payload) {
			t.Errorf("L2 received re-marshalled payload\n got: %s\nwant: %s", bodies[0], payload)
		}
		if tokens[0] != "Bearer "+l2TestToken {
			t.Errorf("Authorization = %q, want Bearer token", tokens[0])
		}
		var id int64
		if _, err := fmt.Sscanf(factIDs[0], "%d", &id); err != nil || id <= 0 {
			t.Errorf("X-Audit-Fact-Id = %q, want positive outbox row id", factIDs[0])
		}
		// Exactly-once post-complete: nothing due remains.
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("delivered fact reclaimed: len=%d err=%v", len(pending), err)
		}
		// A second batch must not re-deliver.
		relay.deliverBatch(ctx)
		time.Sleep(50 * time.Millisecond)
		if got := target.count(); got != 1 {
			t.Fatalf("POSTs after second batch = %d, want 1", got)
		}
	})

	t.Run("2xx without echo receipt schedules backoff retry (D5)", func(t *testing.T) {
		repo := openRelayTestRepo(t)
		obj := seedRelayObject(t, repo, "l2/noecho.txt")
		seedDeletedFact(t, repo, obj, fixedDeletedPayload(obj))
		target := &l2SinkTarget{echo: false} // 200 but no X-Audit-Fact-Id echo
		relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{})
		defer srv.Close()

		relay.deliverBatch(ctx)
		waitForTarget(t, target, 1, 5*time.Second)

		// The echo mismatch must schedule a retry: not immediately reclaimable.
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("echo-mismatch fact immediately reclaimable: len=%d err=%v", len(pending), err)
		}
		// After the backoff (attempt=1 → 1s ±25%) the next batch re-delivers;
		// the target now echoes, so the fact completes.
		target.mu.Lock()
		target.echo = true
		target.mu.Unlock()
		time.Sleep(1300 * time.Millisecond)
		relay.deliverBatch(ctx)
		waitForTarget(t, target, 2, 5*time.Second)
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token-2", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("fact not completed after echo retry: len=%d err=%v", len(pending), err)
		}
	})

	t.Run("500 response schedules backoff retry and eventual delivery", func(t *testing.T) {
		repo := openRelayTestRepo(t)
		obj := seedRelayObject(t, repo, "l2/5xx.txt")
		seedDeletedFact(t, repo, obj, fixedDeletedPayload(obj))
		target := &l2SinkTarget{echo: true}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if target.count() == 0 {
				target.mu.Lock()
				target.status = http.StatusInternalServerError
				target.mu.Unlock()
			}
			target.handler(w, r)
		}))
		defer srv.Close()
		sink, err := NewAuditSinkL2(srv.URL, map[string]string{"default": l2TestToken}, &http.Client{Timeout: 5 * time.Second}, nil)
		if err != nil {
			t.Fatal(err)
		}
		relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
			PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3, AuditSink: sink,
		})

		relay.deliverBatch(ctx)
		waitForTarget(t, target, 1, 5*time.Second)
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("5xx fact immediately reclaimable: len=%d err=%v", len(pending), err)
		}
		time.Sleep(1300 * time.Millisecond)
		relay.deliverBatch(ctx)
		waitForTarget(t, target, 2, 5*time.Second)
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token-2", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("fact not completed after 5xx retry: len=%d err=%v", len(pending), err)
		}
	})

	t.Run("lease-expiry re-claim re-delivers the same fact (at-least-once)", func(t *testing.T) {
		repo := openRelayTestRepo(t)
		obj := seedRelayObject(t, repo, "l2/reclaim.txt")
		payload := fixedDeletedPayload(obj)
		seedDeletedFact(t, repo, obj, payload)
		target := &l2SinkTarget{echo: true}
		relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{
			ClaimTTL: 50 * time.Millisecond, // POST delay outlives the lease (deliberate counter-example config, C6c)
		})
		defer srv.Close()
		target.delay = 100 * time.Millisecond // first POST crosses the lease boundary

		relay.deliverBatch(ctx)
		waitForTarget(t, target, 1, 5*time.Second)
		target.delay = 0
		// Claim-lost on complete: the fact stays inflight with an expired lease;
		// the next batch reclaims and re-POSTs (designed at-least-once).
		relay.deliverBatch(ctx)
		waitForTarget(t, target, 2, 5*time.Second)
		bodies, factIDs, _ := target.snapshot()
		if string(bodies[0]) != string(bodies[1]) {
			t.Errorf("re-delivery changed the payload bytes (verbatim violated)")
		}
		if factIDs[0] != factIDs[1] {
			t.Errorf("re-delivery changed X-Audit-Fact-Id: %q vs %q (dedupe key must be stable)", factIDs[0], factIDs[1])
		}
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("fact not delivered after re-claim: len=%d err=%v", len(pending), err)
		}
	})

	t.Run("receiver contract: 2xx without echo on a lease-loss re-POST does not complete", func(t *testing.T) {
		// Receiver contract (B): the L2 receiver must echo X-Audit-Fact-Id on
		// EVERY 2xx, including re-POSTs after lease loss. A receiver that
		// echoes only the first POST silently drops the at-least-once window —
		// this test pins that the relay requires the echo on the re-POST too.
		repo := openRelayTestRepo(t)
		obj := seedRelayObject(t, repo, "l2/receipt-contract.txt")
		payload := fixedDeletedPayload(obj)
		seedDeletedFact(t, repo, obj, payload)
		target := &l2SinkTarget{echo: true}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// First POST echoes; every later 2xx (the lease-loss re-POST) does not.
			if target.count() >= 1 {
				target.mu.Lock()
				target.echo = false
				target.mu.Unlock()
			}
			target.handler(w, r)
		}))
		defer srv.Close()
		sink, err := NewAuditSinkL2(srv.URL, map[string]string{"default": l2TestToken}, &http.Client{Timeout: 5 * time.Second}, nil)
		if err != nil {
			t.Fatal(err)
		}
		relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
			PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 50 * time.Millisecond,
			MaxAttempts: 3, AuditSink: sink,
		})
		target.delay = 100 * time.Millisecond // first POST crosses the lease

		relay.deliverBatch(ctx)
		waitForTarget(t, target, 1, 5*time.Second)
		target.delay = 0
		// Lease-loss re-POST arrives as 2xx WITHOUT echo → must NOT complete;
		// backoff is scheduled (not immediately reclaimable).
		relay.deliverBatch(ctx)
		waitForTarget(t, target, 2, 5*time.Second)
		bodies, factIDs, _ := target.snapshot()
		if string(bodies[0]) != string(bodies[1]) || factIDs[0] != factIDs[1] {
			t.Errorf("re-POST changed payload or X-Audit-Fact-Id (dedupe key must be stable)")
		}
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("echo-less re-POST completed the fact: len=%d err=%v", len(pending), err)
		}
		// Receiver contract restored: echo on the re-POST lets the fact complete.
		target.mu.Lock()
		target.echo = true
		target.mu.Unlock()
		time.Sleep(2600 * time.Millisecond) // attempt 2 backoff = 2s ± jitter
		relay.deliverBatch(ctx)
		waitForTarget(t, target, 3, 5*time.Second)
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token-2", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("fact not completed once echo returned on re-POST: len=%d err=%v", len(pending), err)
		}
	})

	t.Run("unbound tenant: zero POSTs, fact still completed (C3)", func(t *testing.T) {
		repo := openRelayTestRepo(t)
		obj := seedRelayObject(t, repo, "l2/unbound.txt")
		seedDeletedFact(t, repo, obj, fixedDeletedPayload(obj))
		target := &l2SinkTarget{echo: true}
		relay, srv := newL2Relay(repo, target, map[string]string{"other-tenant": l2TestToken}, EventOutboxRelayOptions{})
		defer srv.Close()

		relay.deliverBatch(ctx)
		time.Sleep(150 * time.Millisecond)
		if got := target.count(); got != 0 {
			t.Fatalf("unbound tenant triggered %d POSTs, want 0", got)
		}
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("unbound fact not completed: len=%d err=%v", len(pending), err)
		}
	})

	t.Run("old fixture without object_id delivers verbatim (C1)", func(t *testing.T) {
		repo := openRelayTestRepo(t)
		obj := seedRelayObject(t, repo, "l2/old.txt")
		// Pre-object_id envelope: schema_version 1.1 but no object_id field.
		const oldDeleted11Fixture = `{"schema_version":"1.1","event_type":"vault.file.deleted@1.1","tenant":"default","bucket":"default","key":"l2/old.txt","version_id":"v-1","size":7,"etag":"etag-1","backend":"local","request_id":"req-old","actor":"alice"}`
		seedDeletedFact(t, repo, obj, []byte(oldDeleted11Fixture))
		target := &l2SinkTarget{echo: true}
		relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{})
		defer srv.Close()

		relay.deliverBatch(ctx)
		waitForTarget(t, target, 1, 5*time.Second)
		bodies, _, _ := target.snapshot()
		if string(bodies[0]) != oldDeleted11Fixture {
			t.Errorf("old fixture not delivered verbatim\n got: %s\nwant: %s", bodies[0], oldDeleted11Fixture)
		}
		if pending, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(pending) != 0 {
			t.Fatalf("old fixture not completed: len=%d err=%v", len(pending), err)
		}
	})
}

// ── H2: 401/403 → immediate terminal failed, no backoff, attempts frozen ────

func TestOutboxRelay_L2UnauthorizedFailsImmediately(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			repo := openRelayTestRepo(t)
			obj := seedRelayObject(t, repo, "l2/rejected.txt")
			seedDeletedFact(t, repo, obj, fixedDeletedPayload(obj))
			target := &l2SinkTarget{status: status}
			relay, srv := newL2Relay(repo, target, map[string]string{"default": l2TestToken}, EventOutboxRelayOptions{})
			defer srv.Close()

			relay.deliverBatch(ctx)
			waitForTarget(t, target, 1, 5*time.Second)

			// Terminal failed: never claimable again, no backoff window to wait.
			if again, err := repo.ClaimEventOutbox(ctx, "observer", "token", 10, time.Minute); err != nil || len(again) != 0 {
				t.Fatalf("rejected fact reclaimable: len=%d err=%v", len(again), err)
			}
			// A second batch must not re-POST (no retry scheduled).
			relay.deliverBatch(ctx)
			time.Sleep(100 * time.Millisecond)
			if got := target.count(); got != 1 {
				t.Fatalf("rejected fact re-POSTed: %d POSTs, want 1", got)
			}
		})
	}
}

// ── AC-4: prune uses the configured retention horizons (F5/F8) ──────────────

// TestOutboxRelay_PruneUsesConfiguredRetention seeds delivered and
// terminal-failed facts at ages straddling configured horizons (DeliveredRetain
// 1h / FailedRetain 2h) and pins that prune() removes exactly the rows beyond
// each horizon — not the hardcoded 24h/7d constants, not everything older than
// "now". A second prune() removes nothing (relay-level idempotency, F8).
func TestOutboxRelay_PruneUsesConfiguredRetention(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "prune.db")
	repo := openRelayTestRepoAt(t, dsn)
	now := time.Now().UTC()

	// a: delivered, beyond the 1h horizon (3h old) → pruned.
	// b: terminal-failed, beyond the 2h horizon (3h old) → pruned.
	// c: delivered, inside the 1h horizon (30min old) → kept.
	// d: terminal-failed, inside the 2h horizon (90min old) → kept.
	a := seedRelayObject(t, repo, "prune/a-delivered-old.txt")
	b := seedRelayObject(t, repo, "prune/b-failed-old.txt")
	c := seedRelayObject(t, repo, "prune/c-delivered-recent.txt")
	d := seedRelayObject(t, repo, "prune/d-failed-recent.txt")
	for _, obj := range []repository.Object{a, b, c, d} {
		seedRelayFacts(t, repo, obj)
	}
	claimed, err := repo.ClaimEventOutbox(ctx, "owner", "token", 10, time.Minute)
	if err != nil || len(claimed) != 8 {
		t.Fatalf("claim: len=%d err=%v", len(claimed), err)
	}
	for _, fact := range claimed {
		switch fact.OriginID {
		case a.ID, c.ID:
			if err := repo.CompleteEventOutbox(ctx, fact.ID, "owner", "token"); err != nil {
				t.Fatalf("complete: %v", err)
			}
		case b.ID, d.ID:
			// Terminal failed: claim already incremented attempts to 1, so
			// maxAttempts=1 lands the fact in 'failed' immediately.
			if err := repo.RetryEventOutbox(ctx, fact.ID, "owner", "token", "boom", now, 1); err != nil {
				t.Fatalf("fail: %v", err)
			}
		}
	}
	backdateEventOutboxAt(t, dsn, "delivered_at_ns", a.ID, now.Add(-3*time.Hour))
	backdateEventOutboxAt(t, dsn, "created_at_ns", b.ID, now.Add(-3*time.Hour))
	backdateEventOutboxAt(t, dsn, "delivered_at_ns", c.ID, now.Add(-30*time.Minute))
	backdateEventOutboxAt(t, dsn, "created_at_ns", d.ID, now.Add(-90*time.Minute))

	relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
		DeliveredRetain: time.Hour, FailedRetain: 2 * time.Hour,
	})
	relay.prune()

	// Exactly the two 3h-old rows are beyond their horizons. If prune had
	// fallen back to the 24h/7d constants it would remove 0; if the horizons
	// collapsed to "now" it would remove all 4. removed==2 pins the
	// configured horizons; count==4 pins that the recent rows were kept.
	n, err := repo.CountEventOutbox(ctx)
	if err != nil {
		t.Fatalf("count after prune: %v", err)
	}
	if n != 4 {
		t.Fatalf("event_outbox has %d rows after prune, want 4 (only the 3h-old rows pruned)", n)
	}
	// Relay-level idempotency: a second prune removes nothing (F8).
	relay.prune()
	if n, err = repo.CountEventOutbox(ctx); err != nil {
		t.Fatalf("count after second prune: %v", err)
	}
	if n != 4 {
		t.Fatalf("second prune removed rows: %d remain, want 4", n)
	}
}

// ── AC-4: zero retention options fall back to the 24h/7d constants (F5) ─────

// TestOutboxRelay_ZeroRetentionFallsBackToDefaults seeds rows at ages
// straddling the package constants (25h/23h delivered, 8d/6d failed) and pins
// that a relay built with zero retains prunes exactly the rows beyond 24h and
// 7d — byte-for-byte the shipped behavior. If the fallback were 0 (cutoff
// "now") prune would remove all 4; if it were 1h it would remove 3; removed
// == 2 pins 24h/168h exactly.
func TestOutboxRelay_ZeroRetentionFallsBackToDefaults(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "zero-prune.db")
	repo := openRelayTestRepoAt(t, dsn)
	now := time.Now().UTC()

	keys := []string{"z/d-25h.txt", "z/d-23h.txt", "z/f-8d.txt", "z/f-6d.txt"}
	objs := make(map[string]repository.Object, len(keys))
	for _, key := range keys {
		objs[key] = seedRelayObject(t, repo, key)
		seedRelayFacts(t, repo, objs[key])
	}
	claimed, err := repo.ClaimEventOutbox(ctx, "owner", "token", 10, time.Minute)
	if err != nil || len(claimed) != 8 {
		t.Fatalf("claim: len=%d err=%v", len(claimed), err)
	}
	for _, fact := range claimed {
		switch fact.OriginID {
		case objs["z/d-25h.txt"].ID, objs["z/d-23h.txt"].ID:
			if err := repo.CompleteEventOutbox(ctx, fact.ID, "owner", "token"); err != nil {
				t.Fatalf("complete: %v", err)
			}
		case objs["z/f-8d.txt"].ID, objs["z/f-6d.txt"].ID:
			if err := repo.RetryEventOutbox(ctx, fact.ID, "owner", "token", "boom", now, 1); err != nil {
				t.Fatalf("fail: %v", err)
			}
		}
	}
	backdateEventOutboxAt(t, dsn, "delivered_at_ns", objs["z/d-25h.txt"].ID, now.Add(-25*time.Hour))
	backdateEventOutboxAt(t, dsn, "delivered_at_ns", objs["z/d-23h.txt"].ID, now.Add(-23*time.Hour))
	backdateEventOutboxAt(t, dsn, "created_at_ns", objs["z/f-8d.txt"].ID, now.Add(-8*24*time.Hour))
	backdateEventOutboxAt(t, dsn, "created_at_ns", objs["z/f-6d.txt"].ID, now.Add(-6*24*time.Hour))

	// Zero retains: NewEventOutboxRelay must fall back to the constants.
	relay := NewEventOutboxRelay(repo, nil, EventOutboxRelayOptions{
		PollInterval: time.Hour, BatchSize: 32, ClaimTTL: 30 * time.Second, MaxAttempts: 3,
	})
	if relay.deliveredRetain != eventOutboxDeliveredRetain {
		t.Errorf("deliveredRetain = %v, want %v", relay.deliveredRetain, eventOutboxDeliveredRetain)
	}
	if relay.failedRetain != eventOutboxFailedRetain {
		t.Errorf("failedRetain = %v, want %v", relay.failedRetain, eventOutboxFailedRetain)
	}
	relay.prune()

	n, err := repo.CountEventOutbox(ctx)
	if err != nil {
		t.Fatalf("count after prune: %v", err)
	}
	// Two origins pruned × 2 facts each = 4 rows; two origins kept = 4 rows.
	// If the fallback were 0 (cutoff "now") prune would remove all 8; if it
	// were 1h it would remove 6 (the 23h-delivered origin too). remain==4
	// pins 24h/168h exactly.
	if n != 4 {
		t.Fatalf("event_outbox has %d rows after prune, want 4 (25h-delivered + 8d-failed origins pruned, 23h/6d kept)", n)
	}
}
