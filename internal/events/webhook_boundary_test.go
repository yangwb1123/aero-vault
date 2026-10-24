package events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func TestWebhookBoundaryInitialAuthority(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	otherID := int64(18)
	cases := []struct {
		name   string
		mutate func(*repository.Event)
	}{
		{"zero_id", func(e *repository.Event) { e.ID = 0 }},
		{"unpersisted", func(e *repository.Event) { e.ID++ }},
		{"gap", func(e *repository.Event) { e.ID-- }},
		{"tenant", func(e *repository.Event) { e.TenantID = "beta" }},
		{"bucket", func(e *repository.Event) { e.Bucket = "other" }},
		{"key", func(e *repository.Event) { e.Key = "other.txt" }},
		{"type", func(e *repository.Event) { e.Type = repository.EventCreated }},
		{"object", func(e *repository.Event) { e.ObjectID = &otherID }},
		{"payload", func(e *repository.Event) { e.Payload = map[string]string{"kind": "forged"} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			want := e
			test.mutate(&want)
			w := f.webhook(t, srv.URL)
			w.deliver(t.Context(), want) // No retry store must not disable admission.
			w.WithRetryStore(f.repo).deliver(t.Context(), want)
		})
	}
	if posts.Load() != 0 {
		t.Fatalf("unverified events produced %d unsafe POSTs", posts.Load())
	}
	webhookTestNoFailures(t, f)
}

func webhookTestNoFailures(t *testing.T, f *webhookFixture) {
	t.Helper()
	rows, err := f.repo.ListWebhookFailures(t.Context(), 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("initial refusal created retry facts: rows=%+v err=%v", rows, err)
	}
}

func TestWebhookBoundaryInitialUsesPersistedEnvelope(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	capt := &captureHandler{}
	srv := httptest.NewServer(capt)
	defer srv.Close()
	w := f.webhook(t, srv.URL)
	want := e
	want.RequestID, want.CreatedAt = "caller-request", time.Unix(1, 0)
	w.deliver(t.Context(), want)
	capt.mu.Lock()
	defer capt.mu.Unlock()
	var got map[string]any
	if err := json.Unmarshal(capt.body, &got); err != nil {
		t.Fatal(err)
	}
	if got["request_id"] != e.RequestID || got["created_at"] != e.CreatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("initial envelope used caller fields: %s", capt.body)
	}
}

func TestWebhookBoundaryMissingPorts(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	var typedNilRepo *webhookTestReadFault
	var typedNilProbe webhookTestProbe
	for _, test := range []struct {
		name  string
		apply func(*Webhook)
	}{
		{"nil_authority", func(w *Webhook) { w.WithEventAuthority(nil) }},
		{"empty_authority", func(w *Webhook) { w.WithEventAuthority(&EventAuthority{}) }},
		{"typed_nil_source", func(w *Webhook) { w.WithEventAuthority(&EventAuthority{repo: typedNilRepo}) }},
		{"nil_boundary", func(w *Webhook) { w.WithResourceBoundary(nil) }},
		{"empty_boundary", func(w *Webhook) { w.WithResourceBoundary(&ResourceBoundary{}) }},
		{"nil_reader", func(w *Webhook) { w.WithResourceBoundary(NewResourceBoundary(nil)) }},
		{"typed_nil_reader", func(w *Webhook) { w.WithResourceBoundary(NewResourceBoundary(typedNilProbe)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := f.webhook(t, srv.URL)
			test.apply(w)
			w.deliver(t.Context(), e)
			w.WithRetryStore(f.repo).deliver(t.Context(), e)
		})
	}
	NewWebhook(srv.URL, nil).WithRetryStore(f.repo).deliver(t.Context(), e)
	if posts.Load() != 0 {
		t.Fatalf("missing ports produced %d unsafe POSTs", posts.Load())
	}
	webhookTestNoFailures(t, f)
}

func TestWebhookBoundaryInitialRefusals(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	disabled := webhookTestUnbound(e.TenantID, e.Bucket)
	disabled.TenantStatus = "disabled"
	for _, test := range []struct {
		name   string
		result spaces.BindingResult
		err    error
	}{
		{"bound", webhookTestBound(e.TenantID, e.Bucket), nil},
		{"disabled", disabled, nil},
		{"zero_result", spaces.BindingResult{}, nil},
		{"wrong_echo", webhookTestUnbound("beta", e.Bucket), nil},
		{"reader_fault", webhookTestUnbound(e.TenantID, e.Bucket), errors.New("private DB detail")},
		{"nested_denial_fault", spaces.BindingResult{}, errors.Join(ErrResourceBoundaryDenied, spaces.ErrNotFound)},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := f.webhook(t, srv.URL)
			w.WithResourceBoundary(NewResourceBoundary(webhookTestProbe(func(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
				if tenant != e.TenantID || bucket != e.Bucket {
					t.Errorf("wrong probe scope: %q/%q", tenant, bucket)
				}
				return test.result, test.err
			})))
			w.deliver(t.Context(), e)
			w.WithRetryStore(f.repo).deliver(t.Context(), e)
		})
	}
	if posts.Load() != 0 {
		t.Fatalf("refused observations produced %d unsafe POSTs", posts.Load())
	}
	webhookTestNoFailures(t, f)
}

func TestWebhookBoundaryFreshAdmissionForEveryURL(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var bound atomic.Bool
	var first, second atomic.Int64
	a := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		first.Add(1)
		bound.Store(true)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { second.Add(1) }))
	defer b.Close()
	w := f.webhook(t, a.URL+","+b.URL)
	w.WithResourceBoundary(NewResourceBoundary(webhookTestProbe(func(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
		if bound.Load() {
			return webhookTestBound(tenant, bucket), nil
		}
		return webhookTestUnbound(tenant, bucket), nil
	})))
	w.deliver(t.Context(), e)
	if first.Load() != 1 || second.Load() != 0 {
		t.Fatalf("binding change was cached across URLs: first=%d second=%d", first.Load(), second.Load())
	}
	bound.Store(false)
	w.urls = []string{b.URL}
	w.deliver(t.Context(), e)
	if second.Load() != 1 {
		t.Fatal("fresh unbound observation did not restore ordinary delivery")
	}
}

func TestWebhookBoundaryFreshAuthorityForEveryURL(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var first, second atomic.Int64
	a := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		first.Add(1)
		if _, err := f.db.ExecContext(t.Context(), `DELETE FROM object_events WHERE id=$1`, e.ID); err != nil {
			t.Error(err)
		}
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { second.Add(1) }))
	defer b.Close()
	f.webhook(t, a.URL+","+b.URL).deliver(t.Context(), e)
	if first.Load() != 1 || second.Load() != 0 {
		t.Fatalf("removed source was reused: first=%d second=%d", first.Load(), second.Load())
	}
}

func TestWebhookBoundaryNilRetryStoreStillDeliversAndChecksFaults(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	w := f.webhook(t, srv.URL).WithRetryStore(nil)
	w.deliver(t.Context(), e)
	if posts.Load() != 1 {
		t.Fatal("valid unbound delivery requires no retry store")
	}
	webhookTestNoFailures(t, f)
	if _, err := f.db.ExecContext(t.Context(), `UPDATE object_events SET payload=$1 WHERE id=$2`, `{"bad":`, e.ID); err != nil {
		t.Fatal(err)
	}
	_, err := w.admitEvent(t.Context(), e)
	var syntax *json.SyntaxError
	if !IsEventAuthorityFault(err) || !errors.As(err, &syntax) {
		t.Fatalf("SQL decode fault lost its cause: %v", err)
	}
	w.deliver(t.Context(), e)
	if posts.Load() != 1 {
		t.Fatal("decode fault escaped through nil retry-store delivery")
	}
}

func TestWebhookBoundaryCancellationAfterObservation(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := f.webhook(t, srv.URL)
	w.WithResourceBoundary(NewResourceBoundary(webhookTestProbe(func(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
		cancel()
		return webhookTestUnbound(tenant, bucket), nil
	})))
	w.deliver(ctx, e)
	if posts.Load() != 0 {
		t.Fatal("cancellation during boundary observation produced an unsafe POST")
	}
}
