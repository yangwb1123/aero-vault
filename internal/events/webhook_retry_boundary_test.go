package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func webhookTestRefusedRetry(t *testing.T, before, after repository.WebhookFailure, dead bool) {
	t.Helper()
	if after.ID != before.ID || after.EventID != before.EventID || after.URL != before.URL || after.Payload != before.Payload {
		t.Fatalf("refusal changed persisted delivery identity/bytes: before=%+v after=%+v", before, after)
	}
	if after.Succeeded || after.DeadLettered != dead || after.Attempts != before.Attempts+1 || after.LastStatus != 0 {
		t.Fatalf("wrong refusal transition: %+v (terminal=%v)", after, dead)
	}
	if !dead && !after.NextRetryAt.After(time.Now()) {
		t.Fatalf("temporary refusal was not rescheduled: %+v", after)
	}
	if after.LastError == "" || strings.Contains(after.LastError, "private DB detail") {
		t.Fatalf("refusal lost classification or exposed cause: %q", after.LastError)
	}
}

func TestWebhookRetryBoundaryMalformedEnvelopesAreTerminal(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	valid := webhookTestEnvelope(t, e)
	for _, body := range []string{
		"", "{}", "null", "{", valid + "{}",
		strings.Replace(valid, `"id":41`, `"id":42`, 1),
		strings.Replace(valid, `"tenant":"alpha"`, `"tenant":""`, 1),
		strings.Replace(valid, `"bucket":"docs"`, `"bucket":""`, 1),
		strings.Replace(valid, `"key":"a.txt"`, `"key":""`, 1),
		strings.Replace(valid, `"type":"deleted"`, `"type":1`, 1),
		strings.Replace(valid, `"object_id":17`, `"object_id":"17"`, 1),
	} {
		t.Run(body, func(t *testing.T) {
			before := f.failure(t, e, srv.URL, body, 2)
			f.webhook(t, srv.URL).WithRetryStore(f.repo).retryOne(t.Context(), before)
			webhookTestRefusedRetry(t, before, f.row(t, before.ID), true)
		})
	}
	if posts.Load() != 0 {
		t.Fatalf("malformed retries produced %d unsafe POSTs", posts.Load())
	}
}

func TestWebhookRetryBoundaryExplicitDenialsAreTerminal(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	disabled := webhookTestUnbound(e.TenantID, e.Bucket)
	disabled.TenantStatus = "disabled"
	for _, result := range []spaces.BindingResult{webhookTestBound(e.TenantID, e.Bucket), disabled} {
		before := f.failure(t, e, srv.URL, webhookTestEnvelope(t, e), 2)
		w := f.webhook(t, srv.URL).WithRetryStore(f.repo)
		w.WithResourceBoundary(NewResourceBoundary(webhookTestProbe(func(context.Context, string, string) (spaces.BindingResult, error) {
			return result, nil
		})))
		w.retryOne(t.Context(), before)
		webhookTestRefusedRetry(t, before, f.row(t, before.ID), true)
	}
	if posts.Load() != 0 {
		t.Fatalf("explicitly refused retries produced %d unsafe POSTs", posts.Load())
	}
}

func webhookTestRetryFaults(t *testing.T, f *webhookFixture) map[string]func(*Webhook) {
	t.Helper()
	return map[string]func(*Webhook){
		"missing_authority": func(w *Webhook) { w.WithEventAuthority(nil) },
		"missing_boundary":  func(w *Webhook) { w.WithResourceBoundary(nil) },
		"authority_fault": func(w *Webhook) {
			w.WithEventAuthority(webhookTestAuthority(t, &webhookTestReadFault{
				Repository: f.repo, err: errors.Join(ErrResourceBoundaryDenied, repository.ErrNotFound, errors.New("private DB detail")),
			}))
		},
		"boundary_fault_over_denied": func(w *Webhook) {
			w.WithResourceBoundary(NewResourceBoundary(webhookTestProbe(func(context.Context, string, string) (spaces.BindingResult, error) {
				return spaces.BindingResult{}, errors.Join(ErrResourceBoundaryDenied, spaces.ErrNotFound, errors.New("private DB detail"))
			})))
		},
		"wrong_echo": func(w *Webhook) {
			w.WithResourceBoundary(NewResourceBoundary(webhookTestProbe(func(context.Context, string, string) (spaces.BindingResult, error) {
				return webhookTestUnbound("beta", "docs"), nil
			})))
		},
	}
}

func TestWebhookRetryBoundaryFaultsStayTemporary(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	for name, configure := range webhookTestRetryFaults(t, f) {
		t.Run(name, func(t *testing.T) {
			before := f.failure(t, e, srv.URL, webhookTestEnvelope(t, e), 2)
			w := f.webhook(t, srv.URL).WithRetryStore(f.repo)
			configure(w)
			w.retryOne(t.Context(), before)
			webhookTestRefusedRetry(t, before, f.row(t, before.ID), false)
		})
	}
	// An intact JSON envelope with a forged fact is unresolved, not authority.
	body := strings.Replace(webhookTestEnvelope(t, e), `"key":"a.txt"`, `"key":"forged.txt"`, 1)
	before := f.failure(t, e, srv.URL, body, 2)
	f.webhook(t, srv.URL).WithRetryStore(f.repo).retryOne(t.Context(), before)
	webhookTestRefusedRetry(t, before, f.row(t, before.ID), false)
	if posts.Load() != 0 {
		t.Fatalf("temporary faults produced %d unsafe POSTs", posts.Load())
	}
}

func TestWebhookRetryBoundarySQLDecodeFaultAndFiniteRetries(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	before := f.failure(t, e, srv.URL, webhookTestEnvelope(t, e), 8)
	if _, err := f.db.ExecContext(t.Context(), `UPDATE object_events SET payload=$1 WHERE id=$2`, `{"bad":`, e.ID); err != nil {
		t.Fatal(err)
	}
	w := f.webhook(t, srv.URL).WithRetryStore(f.repo)
	w.retryOne(t.Context(), before)
	after := f.row(t, before.ID)
	webhookTestRefusedRetry(t, before, after, false)
	w.retryOne(t.Context(), after)
	terminal := f.row(t, before.ID)
	webhookTestRefusedRetry(t, after, terminal, true)
	if terminal.Attempts != 10 || !strings.Contains(terminal.LastError, "dead-lettered after 10 attempts") || posts.Load() != 0 {
		t.Fatalf("fault bypassed finite retry policy: %+v posts=%d", terminal, posts.Load())
	}
}

func TestWebhookRetryBoundaryRechecksBindingAfterInitialFailure(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	w := f.webhook(t, srv.URL).WithRetryStore(f.repo)
	w.deliver(t.Context(), e)
	pending, err := f.repo.ListWebhookFailures(t.Context(), 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("initial failure not persisted: %+v %v", pending, err)
	}
	before := pending[0]
	w.WithResourceBoundary(NewResourceBoundary(webhookTestProbe(func(context.Context, string, string) (spaces.BindingResult, error) {
		return webhookTestBound(e.TenantID, e.Bucket), nil
	})))
	w.retryOne(t.Context(), before)
	webhookTestRefusedRetry(t, before, f.row(t, before.ID), true)
	if posts.Load() != 1 {
		t.Fatalf("binding change allowed unsafe retry POST: %d", posts.Load())
	}
}

type webhookTestRetryRequest struct {
	body, signature, eventID, attempt string
}

func webhookTestSignedRetry(t *testing.T, got webhookTestRetryRequest, body, attempt string) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte("retry-secret"))
	mac.Write([]byte(body))
	if got.body != body || got.signature != "sha256="+hex.EncodeToString(mac.Sum(nil)) || got.eventID != "41" || got.attempt != attempt {
		t.Fatalf("retry changed wire bytes/HMAC/headers: %+v", got)
	}
}

func TestWebhookRetryBoundaryPreservesBytesAndHMAC(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	const body = " \n { \"type\":\"deleted\", \"id\":41, \"tenant\":\"alpha\", \"bucket\":\"docs\", \"key\":\"a.txt\", \"object_id\":17, \"payload\":{\"kind\":\"wanted\"}, \"request_id\":\"original-wire\", \"created_at\":\"2024-01-01T00:00:00Z\", \"future\":true } \n"
	requests := make(chan webhookTestRetryRequest, 2)
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		requests <- webhookTestRetryRequest{string(b), r.Header.Get("X-Aero-Signature"), r.Header.Get("X-Aero-Event-Id"), r.Header.Get("X-Aero-Retry-Attempt")}
		if posts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	before := f.failure(t, e, srv.URL, body, 2)
	w := f.webhook(t, srv.URL).WithRetryStore(f.repo).WithSecret("retry-secret")
	w.retryOne(t.Context(), before)
	after := f.row(t, before.ID)
	if after.Payload != body || after.Attempts != 3 || after.Succeeded || after.DeadLettered || after.LastStatus != 500 {
		t.Fatalf("failed POST did not retain retry envelope: %+v", after)
	}
	w.retryOne(t.Context(), after)
	done := f.row(t, before.ID)
	if !done.Succeeded || done.DeadLettered || done.Payload != body || posts.Load() != 2 {
		t.Fatalf("retry success lost bytes or state: %+v posts=%d", done, posts.Load())
	}
	webhookTestSignedRetry(t, <-requests, body, "3")
	webhookTestSignedRetry(t, <-requests, body, "4")
}

func TestWebhookRetryBoundaryTransitionFailuresRemainPending(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	for _, kind := range []string{"dead-lettered", "retry", "succeeded"} {
		t.Run(kind, func(t *testing.T) { webhookTestFailedTransition(t, f, e, kind) })
	}
}

func webhookTestFailedTransition(t *testing.T, f *webhookFixture, e repository.Event, kind string) {
	t.Helper()
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	defer srv.Close()
	before := f.failure(t, e, srv.URL, webhookTestEnvelope(t, e), 2)
	store := &webhookTestTransitions{Repository: f.repo, fail: kind}
	var logs bytes.Buffer
	w := f.webhook(t, srv.URL).WithRetryStore(store)
	w.logger = slog.New(slog.NewTextHandler(&logs, nil))
	switch kind {
	case "dead-lettered":
		w.WithResourceBoundary(NewResourceBoundary(webhookTestProbe(func(context.Context, string, string) (spaces.BindingResult, error) {
			return webhookTestBound(e.TenantID, e.Bucket), nil
		})))
	case "retry":
		w.WithResourceBoundary(nil)
	}
	w.retryOne(t.Context(), before)
	if after := f.row(t, before.ID); after != before {
		t.Fatalf("failed transition mutated durable state: before=%+v after=%+v", before, after)
	}
	if store.succeeded+store.dead+store.reschedule != 1 || !strings.Contains(logs.String(), "webhook "+kind) {
		t.Fatalf("transition failure was hidden or fell back: store=%+v logs=%s", store, &logs)
	}
	wantPosts := int64(0)
	if kind == "succeeded" {
		wantPosts = 1
	}
	if posts.Load() != wantPosts {
		t.Fatalf("wrong egress after failed transition: %d, want %d", posts.Load(), wantPosts)
	}
}

func TestWebhookRetryBoundaryMalformedURLReachesBackoff(t *testing.T) {
	f := newWebhookFixture(t)
	e := webhookTestEvent()
	f.persist(t, e)
	before := f.failure(t, e, "://not a url", webhookTestEnvelope(t, e), 2)
	f.webhook(t, "").WithRetryStore(f.repo).retryOne(t.Context(), before)
	after := f.row(t, before.ID)
	if after.Succeeded || after.DeadLettered || after.Attempts != 3 || !after.NextRetryAt.After(time.Now()) || !strings.HasPrefix(after.LastError, "bad url: ") {
		t.Fatalf("malformed URL was masked by admission instead of reaching backoff: %+v", after)
	}
}
