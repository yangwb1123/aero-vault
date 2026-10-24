package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func TestRuleMatches(t *testing.T) {
	tests := []struct {
		name      string
		rule      repository.NotificationRule
		eventName string
		key       string
		want      bool
	}{
		{
			name: "exact match",
			rule: repository.NotificationRule{
				Events: []string{"s3:ObjectCreated:Put"},
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "file.txt",
			want:      true,
		},
		{
			name: "wildcard match",
			rule: repository.NotificationRule{
				Events: []string{"s3:ObjectCreated:*"},
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "file.txt",
			want:      true,
		},
		{
			name: "no match different event",
			rule: repository.NotificationRule{
				Events: []string{"s3:ObjectCreated:Put"},
			},
			eventName: "s3:ObjectRemoved:Delete",
			key:       "file.txt",
			want:      false,
		},
		{
			name: "filter key prefix match",
			rule: repository.NotificationRule{
				Events:    []string{"s3:ObjectCreated:*"},
				FilterKey: "images/",
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "images/photo.jpg",
			want:      true,
		},
		{
			name: "filter key prefix no match",
			rule: repository.NotificationRule{
				Events:    []string{"s3:ObjectCreated:*"},
				FilterKey: "images/",
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "docs/file.txt",
			want:      false,
		},
		{
			name: "empty events",
			rule: repository.NotificationRule{
				Events: []string{},
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "file.txt",
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ruleMatches(tt.rule, tt.eventName, tt.key)
			if got != tt.want {
				t.Errorf("ruleMatches(%v, %q, %q) = %v, want %v", tt.rule, tt.eventName, tt.key, got, tt.want)
			}
		})
	}
}

func TestS3EventName(t *testing.T) {
	if got := s3EventName(repository.EventCreated); got != "s3:ObjectCreated:Put" {
		t.Errorf("EventCreated -> %q", got)
	}
	if got := s3EventName(repository.EventDeleted); got != "s3:ObjectRemoved:Delete" {
		t.Errorf("EventDeleted -> %q", got)
	}
	if got := s3EventName("unknown"); got != "" {
		t.Errorf("unknown -> %q", got)
	}
}

func TestBuildS3Event(t *testing.T) {
	e := repository.Event{ID: 42, TenantID: "t1", Bucket: "b1", Key: "k1", Type: repository.EventCreated}
	ev := buildS3Event(e, "s3:ObjectCreated:Put")
	if len(ev.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(ev.Records))
	}
	r := ev.Records[0]
	if r.EventName != "s3:ObjectCreated:Put" {
		t.Errorf("event name = %q", r.EventName)
	}
	if r.S3.Bucket.Name != "b1" {
		t.Errorf("bucket = %q", r.S3.Bucket.Name)
	}
	if r.S3.Object.Key != "k1" {
		t.Errorf("object key = %q", r.S3.Object.Key)
	}
}

func TestArnToHTTP(t *testing.T) {
	arn := "arn:aws:sns:us-east-1:123456789012:MyTopic"
	url := arnToHTTP(arn, "sns")
	if url == "" || url == arn {
		t.Errorf("sns conversion failed: %q", url)
	}
	if !contains(url, "MyTopic") {
		t.Errorf("expected MyTopic in url: %s", url)
	}
}

// notifierTestReceiver starts the real httptest receiver used by the delivery
// fixtures. The buffered channel keeps the handler non-blocking for one POST.
func notifierTestReceiver(t *testing.T) (<-chan s3NotificationEvent, *httptest.Server) {
	t.Helper()
	received := make(chan s3NotificationEvent, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event s3NotificationEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err == nil {
			received <- event
		}
		w.WriteHeader(http.StatusOK)
	}))
	return received, srv
}

// notifierTestPreparationContext bounds repository preparation with the
// testing.T context and the package -timeout deadline, so a hung Open, Migrate
// or rule write is canceled instead of hiding behind a liveness-free context.
func notifierTestPreparationContext(t *testing.T) context.Context {
	t.Helper()
	ctx := t.Context()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		t.Cleanup(cancel)
	}
	return ctx
}

// notifierTestPrepare performs the real on-disk SQLite Open, the full Migrate,
// a real persisted delivery event plus its authoritative reread, and the
// notification rule write. setupDelay is a test-only controlled preparation
// barrier for the timer-boundary regression; zero keeps the normal fixture
// unchanged.
func notifierTestPrepare(t *testing.T, endpointURL string, setupDelay time.Duration) (repository.Repository, repository.Event) {
	t.Helper()
	prepCtx := notifierTestPreparationContext(t)
	prepStart := time.Now()

	dbPath := t.TempDir() + "/test.db"
	repo, err := repository.Open(prepCtx, "sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	if err := repo.Migrate(prepCtx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := repo.InsertEvent(prepCtx, notifierTestEvent()); err != nil {
		t.Fatalf("insert delivery event: %v", err)
	}
	persisted := notifierTestPersistedEvent(t, prepCtx, repo)
	err = repo.SetBucketNotifications(prepCtx, "default", "default", []repository.NotificationRule{{
		ID:          "test-rule",
		Events:      []string{"s3:ObjectCreated:*"},
		EndpointURL: endpointURL,
	}})
	if err != nil {
		t.Fatalf("set notifications: %v", err)
	}
	if setupDelay > 0 {
		time.Sleep(setupDelay)
	}
	if elapsed := time.Since(prepStart); setupDelay > 0 && elapsed <= 5*time.Second {
		t.Fatalf("preparation elapsed %v; boundary regression requires > 5s", elapsed)
	}
	return repo, persisted
}

func notifierTestEvent() repository.Event {
	return repository.Event{
		TenantID: "default", Bucket: "default", Key: "test-file.txt",
		Type: repository.EventCreated, Payload: map[string]string{"size": "1"},
	}
}

func notifierTestPersistedEvent(t *testing.T, ctx context.Context, repo repository.Repository) repository.Event {
	t.Helper()
	rows, err := repo.ListEventsAfter(ctx, "default", 0, 1)
	if err != nil {
		t.Fatalf("reread delivery event: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 1 {
		t.Fatalf("persisted event = %+v, want ID 1", rows)
	}
	return rows[0]
}

// notifierTestDeliver runs the actual Notifier against the prepared repository
// under the supplied delivery context and waits for the original event.
func notifierTestDeliver(t *testing.T, repo repository.Repository, received <-chan s3NotificationEvent, ctx context.Context, event repository.Event) s3NotificationEvent {
	t.Helper()
	authority, err := NewEventAuthority(repo)
	if err != nil {
		t.Fatalf("new event authority: %v", err)
	}
	boundary, err := NewResourceBoundaryFromRepository(repo)
	if err != nil {
		t.Fatalf("new resource boundary: %v", err)
	}
	n := NewNotifier(repo, nil).WithEventAuthority(authority).WithResourceBoundary(boundary)
	sub := make(chan repository.Event, 1)
	go n.Run(ctx, sub)

	sub <- event
	select {
	case got := <-received:
		return got
	case <-ctx.Done():
		t.Fatal("timeout waiting for notification delivery")
		return s3NotificationEvent{}
	}
}

// notifierTestDelivery defines the shared fixture: preparation runs first
// under the test context/deadline, and only after it succeeds does a fresh 5s
// window start for the real Notifier run, rule read, HTTP POST and assertions.
func notifierTestDelivery(t *testing.T, setupDelay time.Duration) {
	t.Helper()
	received, srv := notifierTestReceiver(t)
	defer srv.Close()

	repo, event := notifierTestPrepare(t, srv.URL, setupDelay)
	defer repo.Close()

	deliveryStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := notifierTestDeliver(t, repo, received, ctx, event)
	notifierTestAssertDelivered(t, got, event)

	if setupDelay > 0 {
		if elapsed := time.Since(deliveryStart); elapsed >= 5*time.Second {
			t.Fatalf("delivery elapsed %v after %v preparation; window was consumed", elapsed, setupDelay)
		}
	}
}

func notifierTestAssertDelivered(t *testing.T, got s3NotificationEvent, event repository.Event) {
	t.Helper()
	if len(got.Records) != 1 {
		t.Fatalf("delivered records = %d, want 1", len(got.Records))
	}
	record := got.Records[0]
	if record.EventName != "s3:ObjectCreated:Put" || record.S3.Bucket.Name != event.Bucket ||
		record.S3.Object.Key != event.Key {
		t.Fatalf("delivered record = %+v", record)
	}
	if record.ResponseElements["x-amz-request-id"] != fmt.Sprintf("%d", event.ID) ||
		record.S3.Object.Sequencer != fmt.Sprintf("%020d", event.ID) {
		t.Fatalf("delivered event identity = %+v", record)
	}
}

func TestNotifierDelivery(t *testing.T) {
	notifierTestDelivery(t, 0)
}

// TestNotifierDeliverySlowSetup is the discriminating regression for the
// setup/delivery timer boundary: a preparation longer than the original 5s
// delivery window must not consume that window.
func TestNotifierDeliverySlowSetup(t *testing.T) {
	notifierTestDelivery(t, 5*time.Second+200*time.Millisecond)
}

func notifierTestNoPost(t *testing.T, boundary *ResourceBoundary, mutate func(*repository.Event)) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	repo, event := notifierTestPrepare(t, srv.URL, 0)
	defer repo.Close()
	if mutate != nil {
		mutate(&event)
	}
	authority, err := NewEventAuthority(repo)
	if err != nil {
		t.Fatalf("new event authority: %v", err)
	}
	n := NewNotifier(repo, nil).WithEventAuthority(authority).WithResourceBoundary(boundary)
	n.deliver(t.Context(), event)
	if got := posts.Load(); got != 0 {
		t.Fatalf("notification POSTs = %d, want 0", got)
	}
	rows, err := repo.ListEventsAfter(t.Context(), "default", 0, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != 1 {
		t.Fatalf("persisted event changed: rows=%+v err=%v", rows, err)
	}
}

func TestNotifierAdmissionNoPost(t *testing.T) {
	t.Run("bound", func(t *testing.T) {
		notifierTestNoPost(t, busTestBoundary(spaces.BindingBound, nil), nil)
	})
	t.Run("fault", func(t *testing.T) {
		notifierTestNoPost(t, busTestBoundary(spaces.BindingUnbound, errors.New("probe fault")), nil)
	})
	t.Run("missing capability", func(t *testing.T) {
		notifierTestNoPost(t, nil, nil)
	})
	t.Run("forged id", func(t *testing.T) {
		notifierTestNoPost(t, busTestBoundary(spaces.BindingUnbound, nil), func(e *repository.Event) { e.ID = 2 })
	})
	t.Run("forged key", func(t *testing.T) {
		notifierTestNoPost(t, busTestBoundary(spaces.BindingUnbound, nil), func(e *repository.Event) { e.Key = "other.txt" })
	})
	t.Run("forged payload", func(t *testing.T) {
		notifierTestNoPost(t, busTestBoundary(spaces.BindingUnbound, nil), func(e *repository.Event) {
			e.Payload = map[string]string{"size": "2"}
		})
	})
}

func TestNotifierMissingInjectionNoPost(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	repo, event := notifierTestPrepare(t, srv.URL, 0)
	defer repo.Close()
	NewNotifier(repo, nil).deliver(t.Context(), event)
	if got := posts.Load(); got != 0 {
		t.Fatalf("notification POSTs without injected authority/boundary = %d, want 0", got)
	}
}

type notifierTestFactRepo struct {
	repository.Repository
	has   bool
	err   error
	calls int
}

func (r *notifierTestFactRepo) HasEventOutboxFact(context.Context, int64, repository.OutboxEventType) (bool, error) {
	r.calls++
	return r.has, r.err
}

func TestNotifierD2LookupFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		has  bool
		err  error
	}{
		{"lookup error", false, errors.New("outbox fault")},
		{"row exists", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notifierTestD2(t, tc.has, tc.err)
		})
	}
}

func notifierTestD2(t *testing.T, has bool, cause error) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	repo, _ := notifierTestPrepare(t, srv.URL, 0)
	defer repo.Close()
	if err := repo.SetBucketNotifications(t.Context(), "default", "default", []repository.NotificationRule{{
		ID: "deleted-rule", Events: []string{"s3:ObjectRemoved:*"}, EndpointURL: srv.URL,
	}}); err != nil {
		t.Fatalf("set deleted notifications: %v", err)
	}
	objID := int64(1)
	deleted := repository.Event{
		TenantID: "default", Bucket: "default", Key: "deleted.txt",
		Type: repository.EventDeleted, ObjectID: &objID,
	}
	id, err := repo.InsertEvent(t.Context(), deleted)
	if err != nil {
		t.Fatalf("insert deleted event: %v", err)
	}
	rows, err := repo.ListEventsAfter(t.Context(), "default", id-1, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("reread deleted event: rows=%+v err=%v", rows, err)
	}
	wrapped := &notifierTestFactRepo{Repository: repo, has: has, err: cause}
	authority, err := NewEventAuthority(wrapped)
	if err != nil {
		t.Fatalf("new event authority: %v", err)
	}
	n := NewNotifier(wrapped, nil).WithEventAuthority(authority).
		WithResourceBoundary(busTestBoundary(spaces.BindingUnbound, nil))
	n.deliver(t.Context(), rows[0])
	if got := posts.Load(); got != 0 {
		t.Fatalf("notification POSTs = %d, want 0", got)
	}
	if wrapped.calls != 1 {
		t.Fatalf("D2 lookups = %d, want 1", wrapped.calls)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
