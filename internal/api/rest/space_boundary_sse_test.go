package rest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/events"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

const (
	sseTestTenant    = "alpha"
	sseTestSpaceID   = "12345678-1234-1234-1234-123456789abc"
	sseTestCommandID = "22345678-1234-1234-1234-123456789abc"
)

type sseBindingFunc func(context.Context, string, string) (spaces.BindingResult, error)

func (f sseBindingFunc) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

func sseUnboundReader() sseBindingFunc {
	return func(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
		return spaces.BindingResult{TenantID: tenant, Bucket: bucket, State: spaces.BindingUnbound}, nil
	}
}

func sseTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func sseTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	repo, err := repository.Open(t.Context(), "sqlite", "file:"+filepath.Join(t.TempDir(), "sse.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	return repo
}

func sseTestBoundSpace(t *testing.T, repo repository.Repository) spaces.Space {
	t.Helper()
	if err := repo.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: sseTestTenant, Status: "active"}); err != nil {
		t.Fatalf("upsert tenant: %v", err)
	}
	store, ok := repo.(spaces.Store)
	if !ok {
		t.Fatal("repository does not implement spaces.Store")
	}
	space, err := store.CreateSpace(t.Context(), spaces.CreateCommand{
		TenantID: sseTestTenant, ID: sseTestSpaceID, Kind: spaces.Team, Name: "sse-bound",
		Actor:     spaces.SubjectRef{Source: "test", Kind: spaces.User, ID: "owner"},
		CommandID: sseTestCommandID, Reason: "sse boundary test", RequestID: "sse-request",
	})
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	return space
}

func sseTestInsertEvent(t *testing.T, repo repository.Repository, bucket, key string) int64 {
	t.Helper()
	id, err := repo.InsertEvent(t.Context(), repository.Event{
		TenantID: sseTestTenant, Bucket: bucket, Key: key, Type: repository.EventCreated,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return id
}

func sseTestRequest(lastID int64) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil)
	req.Header.Set(mw.TenantHeader, sseTestTenant)
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(lastID, 10))
	}
	return req
}

func sseTestStream(t *testing.T, h *SSEHandler, lastID int64) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mw.Tenant(http.HandlerFunc(h.Stream)).ServeHTTP(w, sseTestRequest(lastID))
	return w
}

func sseTestAssertNoEvent(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	if strings.Contains(body, "id: ") || strings.Contains(body, "data: ") {
		t.Fatalf("refused stream wrote an event: %q", body)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(body, value) {
			t.Fatalf("refused stream leaked %q: %q", value, body)
		}
	}
}

func TestSSESpaceBoundaryInitialRejectionBeforeHeaders(t *testing.T) {
	repo := sseTestRepo(t)
	space := sseTestBoundSpace(t, repo)
	first := sseTestInsertEvent(t, repo, "plain", "first")
	sseTestInsertEvent(t, repo, space.BackingBucket, "bound")
	h := NewSSEHandler(events.New(repo, sseTestLogger()), repo, sseTestLogger())

	w := sseTestStream(t, h, first)
	if w.Code != http.StatusForbidden {
		t.Fatalf("initial bound event status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") == "text/event-stream" {
		t.Fatal("initial refusal sent SSE headers")
	}
	sseTestAssertNoEvent(t, w.Body.String(), space.BackingBucket, "bound")
}

func TestSSESpaceBoundaryCursorOnlyRejectionBeforeHeaders(t *testing.T) {
	repo := sseTestRepo(t)
	space := sseTestBoundSpace(t, repo)
	cursor := sseTestInsertEvent(t, repo, space.BackingBucket, "cursor-bound")
	h := NewSSEHandler(events.New(repo, sseTestLogger()), repo, sseTestLogger())

	w := sseTestStream(t, h, cursor)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cursor-only bound event status=%d body=%s", w.Code, w.Body.String())
	}
	sseTestAssertNoEvent(t, w.Body.String(), space.BackingBucket, "cursor-bound")
}

func TestSSESpaceBoundaryBoundCursorRejectionBeforeHeaders(t *testing.T) {
	repo := sseTestRepo(t)
	space := sseTestBoundSpace(t, repo)
	cursor := sseTestInsertEvent(t, repo, space.BackingBucket, "cursor-bound")
	sseTestInsertEvent(t, repo, "plain", "new-unbound")
	h := NewSSEHandler(events.New(repo, sseTestLogger()), repo, sseTestLogger())

	w := sseTestStream(t, h, cursor)
	if w.Code != http.StatusForbidden {
		t.Fatalf("bound cursor status=%d body=%s", w.Code, w.Body.String())
	}
	sseTestAssertNoEvent(t, w.Body.String(), space.BackingBucket, "new-unbound")
}

func TestSSESpaceBoundaryInitialFaultBeforeHeaders(t *testing.T) {
	repo := sseTestRepo(t)
	first := sseTestInsertEvent(t, repo, "plain", "first")
	sseTestInsertEvent(t, repo, "plain", "next")
	boom := errors.New("binding store secret")
	for _, tc := range []struct {
		name   string
		reader spaces.BindingReader
	}{
		{"missing", nil},
		{"typed-nil", sseBindingFunc(nil)},
		{"fault", sseBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) {
			return spaces.BindingResult{}, boom
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewSSEHandler(events.New(repo, sseTestLogger()), repo, sseTestLogger()).
				WithResourceBoundary(tc.reader)
			w := sseTestStream(t, h, first)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("fault text leaked: %q", w.Body.String())
			}
			sseTestAssertNoEvent(t, w.Body.String())
		})
	}
}

func TestSSESpaceBoundaryBacklogRejectionStopsReplay(t *testing.T) {
	repo := sseTestRepo(t)
	space := sseTestBoundSpace(t, repo)
	first := sseTestInsertEvent(t, repo, "plain", "first")
	second := sseTestInsertEvent(t, repo, "plain", "second")
	sseTestInsertEvent(t, repo, space.BackingBucket, "bound")
	h := NewSSEHandler(events.New(repo, sseTestLogger()), repo, sseTestLogger())

	w := sseTestStream(t, h, first)
	if w.Code != http.StatusOK {
		t.Fatalf("backlog stream status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "id: "+strconv.FormatInt(second, 10)+"\n") {
		t.Fatalf("admitted backlog event missing: %q", body)
	}
	if strings.Contains(body, space.BackingBucket) || strings.Contains(body, `"key":"bound"`) {
		t.Fatalf("bound backlog event was written: %q", body)
	}
}

type sseReplayRepo struct {
	repository.Repository
	rows []repository.Event
	err  error
}

func (r *sseReplayRepo) ListEventsAfter(context.Context, string, int64, int) ([]repository.Event, error) {
	return r.rows, r.err
}

func TestSSESpaceBoundaryReplayRejectsAuthoritativeMismatch(t *testing.T) {
	repo := sseTestRepo(t)
	boom := errors.New("replay store secret")
	for _, tc := range []struct {
		name string
		rows []repository.Event
		err  error
	}{
		{"tenant", []repository.Event{{ID: 2, TenantID: "beta", Bucket: "plain", Key: "x", Type: repository.EventCreated}}, nil},
		{"id-regression", []repository.Event{{ID: 1, TenantID: sseTestTenant, Bucket: "plain", Key: "x", Type: repository.EventCreated}}, nil},
		{"fault", nil, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &sseReplayRepo{Repository: repo, rows: tc.rows, err: tc.err}
			h := NewSSEHandler(nil, fake, sseTestLogger()).WithResourceBoundary(sseUnboundReader())
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			last, err := h.replayMissed(rec, rec, req, sseTestTenant, 1)
			if err == nil {
				t.Fatal("authoritative mismatch or fault was accepted")
			}
			if last != 1 {
				t.Fatalf("last id=%d, want 1", last)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("fault cause lost: %v", err)
			}
			sseTestAssertNoEvent(t, rec.Body.String())
		})
	}
}

func TestSSESpaceBoundaryLiveRejectionAndAuthorityMismatch(t *testing.T) {
	repo := sseTestRepo(t)
	space := sseTestBoundSpace(t, repo)
	boundID := sseTestInsertEvent(t, repo, space.BackingBucket, "bound")
	realID := sseTestInsertEvent(t, repo, "plain", "real")
	for _, tc := range []struct {
		name  string
		event repository.Event
	}{
		{"bound", repository.Event{ID: boundID, TenantID: sseTestTenant, Bucket: space.BackingBucket, Key: "bound", Type: repository.EventCreated}},
		{"forged-key", repository.Event{ID: realID, TenantID: sseTestTenant, Bucket: "plain", Key: "forged", Type: repository.EventCreated}},
		{"unpersisted", repository.Event{ID: 999999, TenantID: sseTestTenant, Bucket: "plain", Key: "missing", Type: repository.EventCreated}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewSSEHandler(nil, repo, sseTestLogger())
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			sub := make(chan repository.Event, 1)
			sub <- tc.event
			h.liveStream(rec, req, rec, sseTestTenant, sub, 0)
			sseTestAssertNoEvent(t, rec.Body.String(), "forged", "missing")
		})
	}
}

func TestSSESpaceBoundaryLiveFaultStopsDelivery(t *testing.T) {
	repo := sseTestRepo(t)
	id := sseTestInsertEvent(t, repo, "plain", "plain")
	h := NewSSEHandler(nil, repo, sseTestLogger()).WithResourceBoundary(sseBindingFunc(
		func(context.Context, string, string) (spaces.BindingResult, error) {
			return spaces.BindingResult{}, errors.New("live binding secret")
		}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	sub := make(chan repository.Event, 1)
	sub <- repository.Event{ID: id, TenantID: sseTestTenant, Bucket: "plain", Key: "plain", Type: repository.EventCreated}
	h.liveStream(rec, req, rec, sseTestTenant, sub, 0)
	sseTestAssertNoEvent(t, rec.Body.String(), "secret")
}

type sseBlockingReplayRepo struct {
	repository.Repository
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *sseBlockingReplayRepo) ListEventsAfter(ctx context.Context, tenant string, afterID int64, limit int) ([]repository.Event, error) {
	if limit == 1 {
		return r.Repository.ListEventsAfter(ctx, tenant, afterID, limit)
	}
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return nil, errors.New("replay stopped")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSSESpaceBoundaryReplayFaultStopsLiveContinuation(t *testing.T) {
	repo := sseTestRepo(t)
	first := sseTestInsertEvent(t, repo, "plain", "first")
	sseTestInsertEvent(t, repo, "plain", "next")
	blocking := &sseBlockingReplayRepo{
		Repository: repo, entered: make(chan struct{}), release: make(chan struct{}),
	}
	bus := events.New(blocking, sseTestLogger())
	h := NewSSEHandler(bus, blocking, sseTestLogger()).WithResourceBoundary(sseUnboundReader())
	req := sseTestRequest(first)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mw.Tenant(http.HandlerFunc(h.Stream)).ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-blocking.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("replay was not entered")
	}
	bus.Publish(t.Context(), repository.Event{
		TenantID: sseTestTenant, Bucket: "plain", Key: "live", Type: repository.EventCreated,
	})
	close(blocking.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream continued after replay fault")
	}
	if strings.Contains(rec.Body.String(), `"key":"live"`) {
		t.Fatalf("live event was delivered after replay fault: %q", rec.Body.String())
	}
}
