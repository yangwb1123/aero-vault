package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
	_ "modernc.org/sqlite"
)

// Every webhook source is a real, isolated disk database with the full schema.
// Retry persistence and authority are deliberately wired as independent ports.
type webhookFixture struct {
	repo repository.Repository
	db   *sql.DB
}

func newWebhookFixture(t *testing.T) *webhookFixture {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "webhook.db")
	repo, err := repository.Open(t.Context(), "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &webhookFixture{repo: repo, db: db}
}

func (f *webhookFixture) persist(t *testing.T, e repository.Event) {
	t.Helper()
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit SQL IDs preserve the old fixed-ID and header oracles. InsertEvent
	// intentionally allocates IDs itself and does not retain caller timestamps.
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO object_events
		(id,tenant_id,bucket,key,type,object_id,request_id,payload,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, e.ID, e.TenantID, e.Bucket,
		e.Key, string(e.Type), e.ObjectID, e.RequestID, string(payload),
		e.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
}

func webhookTestAuthority(t *testing.T, repo repository.Repository) *EventAuthority {
	t.Helper()
	authority, err := NewEventAuthority(repo)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func (f *webhookFixture) wire(t *testing.T, w *Webhook) {
	t.Helper()
	boundary, err := NewResourceBoundaryFromRepository(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	w.WithEventAuthority(webhookTestAuthority(t, f.repo)).WithResourceBoundary(boundary)
}

func webhookTestWire(t *testing.T, w *Webhook, e repository.Event) *webhookFixture {
	t.Helper()
	f := newWebhookFixture(t)
	f.persist(t, e)
	f.wire(t, w)
	return f
}

func (f *webhookFixture) webhook(t *testing.T, url string) *Webhook {
	t.Helper()
	w := NewWebhook(url, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.wire(t, w)
	return w
}

func webhookTestEvent() repository.Event {
	objectID := int64(17)
	return repository.Event{
		ID: 41, TenantID: "alpha", Bucket: "docs", Key: "a.txt",
		Type: repository.EventDeleted, ObjectID: &objectID,
		RequestID: "persisted-request", Payload: map[string]string{"kind": "wanted"},
		CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 6, time.UTC),
	}
}

func webhookTestEnvelope(t *testing.T, e repository.Event) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": e.ID, "tenant": e.TenantID, "bucket": e.Bucket, "key": e.Key,
		"type": e.Type, "object_id": e.ObjectID, "request_id": e.RequestID,
		"payload": e.Payload, "created_at": e.CreatedAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func webhookTestRetryEnvelope(t *testing.T, w *Webhook) string {
	t.Helper()
	e := webhookTestEvent()
	e.ID = 1
	webhookTestWire(t, w, e)
	return webhookTestEnvelope(t, e)
}

func (f *webhookFixture) failure(t *testing.T, e repository.Event, url, body string, attempts int) repository.WebhookFailure {
	t.Helper()
	id, err := f.repo.RecordWebhookFailure(t.Context(), repository.WebhookFailure{
		EventID: e.ID, URL: url, Payload: body, Attempts: attempts,
		LastError: "original failure", LastStatus: 500,
		NextRetryAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return f.row(t, id)
}

func (f *webhookFixture) row(t *testing.T, id int64) repository.WebhookFailure {
	t.Helper()
	rows, err := f.repo.ListWebhookFailures(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("webhook failure %d is missing", id)
	return repository.WebhookFailure{}
}

func webhookTestUnbound(tenant, bucket string) spaces.BindingResult {
	return spaces.BindingResult{
		TenantID: tenant, Bucket: bucket, TenantKnown: true,
		TenantStatus: "active", State: spaces.BindingUnbound,
	}
}

func webhookTestBound(tenant, bucket string) spaces.BindingResult {
	result := webhookTestUnbound(tenant, bucket)
	result.State, result.SpaceID, result.Revision = spaces.BindingBound,
		"12345678-1234-1234-1234-123456789abc", 1
	return result
}

type webhookTestProbe func(context.Context, string, string) (spaces.BindingResult, error)

func (f webhookTestProbe) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

type webhookTestReadFault struct {
	repository.Repository
	err error
}

func (r *webhookTestReadFault) ListEventsAfter(context.Context, string, int64, int) ([]repository.Event, error) {
	return nil, r.err
}

// captureHandler records the last request received by the mock server.
type captureHandler struct {
	mu     sync.Mutex
	body   []byte
	header http.Header
}

func (h *captureHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.body, _ = io.ReadAll(r.Body)
	h.header = r.Header.Clone()
	rw.WriteHeader(http.StatusOK)
}

// recordFailureRepo records calls to RecordWebhookFailure for assertions.
type recordFailureRepo struct {
	repository.Repository // nil embedded interface: any unexpected call panics
	mu                    sync.Mutex
	failures              []repository.WebhookFailure
}

func (r *recordFailureRepo) RecordWebhookFailure(_ context.Context, f repository.WebhookFailure) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, f)
	return int64(len(r.failures)), nil
}

var webhookTestTransitionFault = errors.New("transition unavailable")

type webhookTestTransitions struct {
	repository.Repository
	fail                        string
	succeeded, dead, reschedule int
}

func (r *webhookTestTransitions) MarkWebhookSucceeded(ctx context.Context, id int64) error {
	r.succeeded++
	if r.fail == "succeeded" {
		return webhookTestTransitionFault
	}
	return r.Repository.MarkWebhookSucceeded(ctx, id)
}

func (r *webhookTestTransitions) MarkWebhookDeadLettered(ctx context.Context, id int64, message string, status, attempts int) error {
	r.dead++
	if r.fail == "dead-lettered" {
		return webhookTestTransitionFault
	}
	return r.Repository.MarkWebhookDeadLettered(ctx, id, message, status, attempts)
}

func (r *webhookTestTransitions) UpdateWebhookFailure(ctx context.Context, id int64, message string, status int, next time.Time, attempts int) error {
	r.reschedule++
	if r.fail == "retry" {
		return webhookTestTransitionFault
	}
	return r.Repository.UpdateWebhookFailure(ctx, id, message, status, next, attempts)
}
