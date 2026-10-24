package events

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// fakeRepo is a minimal Repository used only to drive Bus.Publish. It embeds
// the full interface so it satisfies the type; every method other than
// InsertEvent panics if the Bus ever calls it (it must not). InsertEvent's
// behavior is configurable to exercise the success and error paths.
type fakeRepo struct {
	repository.Repository // nil embedded interface: any unexpected call panics

	mu       sync.Mutex
	inserted []repository.Event
	nextID   int64
	err      error
}

func (f *fakeRepo) InsertEvent(_ context.Context, e repository.Event) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.inserted = append(f.inserted, e)
	f.nextID++
	return f.nextID, nil
}

func (f *fakeRepo) insertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inserted)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recv waits up to d for an event on ch.
func recv(t *testing.T, ch <-chan repository.Event, d time.Duration) (repository.Event, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(d):
		t.Fatalf("timed out waiting for event after %s", d)
		return repository.Event{}, false
	}
}

// expectNothing asserts no event arrives within d.
func expectNothing(t *testing.T, ch <-chan repository.Event, d time.Duration) {
	t.Helper()
	select {
	case e, ok := <-ch:
		if ok {
			t.Fatalf("expected no event, but received %+v", e)
		}
		// channel closed is also "nothing more" — acceptable
	case <-time.After(d):
	}
}

func TestNew_NilLoggerDefaults(t *testing.T) {
	b := New(&fakeRepo{}, nil)
	if b == nil {
		t.Fatal("New returned nil")
	}
	if b.logger == nil {
		t.Fatal("nil logger should default to slog.Default(), got nil")
	}
}

func TestPublish_DeliversToSubscriber(t *testing.T) {
	repo := &fakeRepo{}
	b := New(repo, quietLogger())
	ch, _ := b.Subscribe()

	in := repository.Event{TenantID: "t1", Bucket: "b", Key: "k", Type: repository.EventCreated}
	b.Publish(context.Background(), in)

	got, ok := recv(t, ch, time.Second)
	if !ok {
		t.Fatal("channel closed unexpectedly")
	}
	if got.Key != "k" || got.Type != repository.EventCreated {
		t.Fatalf("received wrong event: %+v", got)
	}
	// Publish stamps the ID returned by InsertEvent (first insert => 1).
	if got.ID != 1 {
		t.Fatalf("broadcast event ID = %d, want 1 (from InsertEvent)", got.ID)
	}
	if repo.insertCount() != 1 {
		t.Fatalf("expected exactly 1 persisted event, got %d", repo.insertCount())
	}
}

func TestPublish_FanOutToMultipleSubscribers(t *testing.T) {
	b := New(&fakeRepo{}, quietLogger())
	const n = 5
	chans := make([]<-chan repository.Event, n)
	for i := range chans {
		chans[i], _ = b.Subscribe()
	}

	b.Publish(context.Background(), repository.Event{Key: "fan", Type: repository.EventDeleted})

	for i, ch := range chans {
		got, ok := recv(t, ch, time.Second)
		if !ok {
			t.Fatalf("subscriber %d: channel closed", i)
		}
		if got.Key != "fan" {
			t.Fatalf("subscriber %d got %+v", i, got)
		}
	}
}

func TestPublish_InsertFailureSuppressesBroadcast(t *testing.T) {
	repo := &fakeRepo{err: context.DeadlineExceeded}
	b := New(repo, quietLogger())
	ch, _ := b.Subscribe()

	// Publish must not panic and must not broadcast when persistence fails.
	b.Publish(context.Background(), repository.Event{Key: "k", Type: repository.EventCreated})

	expectNothing(t, ch, 50*time.Millisecond)
}

func TestPublishFailureLogDoesNotExposeIdentifiersOrErrorText(t *testing.T) {
	const raw = "tenant-secret raw/key bearer-token client-secret"
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	repo := &fakeRepo{err: errors.New(raw)}
	b := New(repo, logger)
	event := repository.Event{
		TenantID: "tenant-secret", Bucket: "private", Key: "raw/key",
		Type: repository.EventCreated, Payload: map[string]string{"token": "bearer-token"},
	}
	b.Publish(context.Background(), event)
	assertEventLogRedacted(t, logs.String(), raw)

	logs.Reset()
	repo.err = nil
	b.WithTransport(func(context.Context, repository.Event) error { return errors.New(raw) })
	b.Publish(context.Background(), event)
	assertEventLogRedacted(t, logs.String(), raw)
}

func assertEventLogRedacted(t *testing.T, logs, raw string) {
	t.Helper()
	for _, forbidden := range strings.Fields(raw) {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("event log leaked %q: %s", forbidden, logs)
		}
	}
}

func TestSubscribe_BufferedAndDropsWhenFull(t *testing.T) {
	// The Bus channel buffer is 64; a slow consumer that never reads should
	// cause overflow events to be dropped (non-blocking broadcast), never
	// blocking Publish. We publish well past the buffer and assert Publish
	// returns promptly each time.
	b := New(&fakeRepo{}, quietLogger())
	ch, _ := b.Subscribe()

	const total = 200 // > buffer (64)
	done := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			b.Publish(context.Background(), repository.Event{Key: "x", Type: repository.EventAccessed})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked: broadcast is not non-blocking under backpressure")
	}

	// Drain whatever buffered (should be capped at the buffer size, 64).
	got := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				goto counted
			}
			got++
		default:
			goto counted
		}
	}
counted:
	if got == 0 {
		t.Fatal("expected some buffered events to be readable")
	}
	if got > 64 {
		t.Fatalf("buffered %d events, want <= 64 (drop-on-overflow semantics)", got)
	}
}

func TestClose_ShutsSubscriberChannels(t *testing.T) {
	b := New(&fakeRepo{}, quietLogger())
	ch1, _ := b.Subscribe()
	ch2, _ := b.Subscribe()

	b.Close()

	// Both channels must now be closed: a receive returns ok=false promptly.
	if _, ok := recv(t, ch1, time.Second); ok {
		t.Fatal("ch1 should be closed after Close()")
	}
	if _, ok := recv(t, ch2, time.Second); ok {
		t.Fatal("ch2 should be closed after Close()")
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	b := New(&fakeRepo{}, quietLogger())
	_, _ = b.Subscribe()
	b.Close()
	// Second Close must not panic (subs are cleared to nil after the first).
	b.Close()
}

func TestPublish_AfterCloseDoesNotPanic(t *testing.T) {
	// After Close clears subscribers, Publish should be a safe no-op broadcast.
	repo := &fakeRepo{}
	b := New(repo, quietLogger())
	_, _ = b.Subscribe()
	b.Close()
	b.Publish(context.Background(), repository.Event{Key: "post-close"})
	if repo.insertCount() != 1 {
		t.Fatalf("Publish after Close should still persist; inserts=%d", repo.insertCount())
	}
}

func TestBus_TransportAdmission(t *testing.T) {
	cases := []struct {
		name      string
		boundary  *ResourceBoundary
		wantCalls int32
	}{
		{"unbound", busTestBoundary(spaces.BindingUnbound, nil), 1},
		{"bound", busTestBoundary(spaces.BindingBound, nil), 0},
		{"fault", busTestBoundary(spaces.BindingUnbound, errors.New("probe fault")), 0},
		{"missing", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			bus := New(repo, quietLogger()).WithResourceBoundary(tc.boundary)
			var calls int32
			bus.WithTransport(func(context.Context, repository.Event) error {
				atomic.AddInt32(&calls, 1)
				return nil
			})
			sub, cancel := bus.Subscribe()
			defer cancel()
			bus.Publish(context.Background(), repository.Event{
				TenantID: "t1", Bucket: "b1", Key: "k", Type: repository.EventCreated,
			})
			if ev, ok := recv(t, sub, time.Second); !ok || ev.Key != "k" {
				t.Fatal("persistence and local broadcast must survive transport admission")
			}
			if repo.insertCount() != 1 || atomic.LoadInt32(&calls) != tc.wantCalls {
				t.Fatalf("inserts=%d transport calls=%d, want 1/%d",
					repo.insertCount(), calls, tc.wantCalls)
			}
		})
	}
}

func TestBus_PublishDoesNotReadAuthorityOrBoundary(t *testing.T) {
	repo := &busTestWrappedRepo{fakeRepo: &fakeRepo{}}
	bus := New(repo, quietLogger())
	sub, cancel := bus.Subscribe()
	defer cancel()
	bus.Publish(context.Background(), repository.Event{
		TenantID: "t1", Bucket: "b1", Key: "local", Type: repository.EventCreated,
	})
	if ev, ok := recv(t, sub, time.Second); !ok || ev.Key != "local" {
		t.Fatal("local publish must still broadcast")
	}
	if repo.listCalls != 0 || repo.probeCalls != 0 || repo.insertCount() != 1 {
		t.Fatalf("list=%d probe=%d inserts=%d; local publish must only insert",
			repo.listCalls, repo.probeCalls, repo.insertCount())
	}
}

func TestBus_WithRepositoryRetainsInjectedCapabilities(t *testing.T) {
	raw := &fakeRepo{}
	authorityRepo := &busTestAuthorityRepo{fakeRepo: raw, rows: []repository.Event{busTestDeliverEvent()}}
	authority, err := NewEventAuthority(authorityRepo)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	bus := New(authorityRepo, quietLogger()).
		WithResourceBoundary(busTestBoundary(spaces.BindingUnbound, nil)).
		WithEventAuthority(authority)
	wrapper := &busTestWrappedRepo{fakeRepo: raw}
	bus.WithRepository(wrapper)
	var transportCalls int32
	bus.WithTransport(func(context.Context, repository.Event) error {
		atomic.AddInt32(&transportCalls, 1)
		return nil
	})
	sub, cancel := bus.Subscribe()
	defer cancel()
	bus.Publish(context.Background(), repository.Event{
		TenantID: "t1", Bucket: "b1", Key: "wrapped", Type: repository.EventCreated,
	})
	if ev, ok := recv(t, sub, time.Second); !ok || ev.Key != "wrapped" {
		t.Fatal("expected locally broadcast event")
	}
	if raw.insertCount() != 1 || atomic.LoadInt32(&transportCalls) != 1 {
		t.Fatalf("wrapper insert=%d transport=%d; want 1/1", raw.insertCount(), transportCalls)
	}
	bus.DeliverContext(context.Background(), busTestDeliverEvent())
	if got, ok := recv(t, sub, time.Second); !ok || got.ID != 1 {
		t.Fatalf("retained authority did not deliver a row: %+v", got)
	}
	if wrapper.listCalls != 0 {
		t.Fatalf("wrapped persistence was reread %d times; raw authority must be retained", wrapper.listCalls)
	}
}

func TestBus_DeliverContextUsesAuthoritativeRow(t *testing.T) {
	want := busTestDeliverEvent()
	row := busTestDeliverEvent()
	row.RequestID = "persisted"
	repo := &busTestAuthorityRepo{fakeRepo: &fakeRepo{}, rows: []repository.Event{row}}
	authority, err := NewEventAuthority(repo)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	bus := New(repo, quietLogger()).WithEventAuthority(authority).
		WithResourceBoundary(busTestBoundary(spaces.BindingUnbound, nil))
	var transportCalls int32
	bus.WithTransport(func(context.Context, repository.Event) error {
		atomic.AddInt32(&transportCalls, 1)
		return nil
	})
	sub, cancel := bus.Subscribe()
	defer cancel()
	bus.DeliverContext(context.Background(), want)
	got, ok := recv(t, sub, time.Second)
	if !ok || got.RequestID != "persisted" || got.Key != want.Key {
		t.Fatalf("delivered row = %+v", got)
	}
	if repo.insertCount() != 0 || atomic.LoadInt32(&transportCalls) != 0 {
		t.Fatalf("deliver persisted=%d transport=%d; want 0/0", repo.insertCount(), transportCalls)
	}
	if repo.listCalls != 1 || repo.lastTenant != want.TenantID || repo.lastAfter != want.ID-1 {
		t.Fatalf("reread args calls=%d tenant=%q after=%d", repo.listCalls, repo.lastTenant, repo.lastAfter)
	}
}

func TestBus_DeliverContextRefusesUnsafeRows(t *testing.T) {
	cases := []struct {
		name      string
		authority bool
		boundary  *ResourceBoundary
		rows      bool
		mutate    func(*repository.Event)
	}{
		{"no_authority", false, busTestBoundary(spaces.BindingUnbound, nil), true, nil},
		{"bound", true, busTestBoundary(spaces.BindingBound, nil), true, nil},
		{"missing_boundary", true, nil, true, nil},
		{"boundary_fault", true, busTestBoundary(spaces.BindingUnbound, errors.New("probe fault")), true, nil},
		{"not_persisted", true, busTestBoundary(spaces.BindingUnbound, nil), false, nil},
		{"forged_id", true, busTestBoundary(spaces.BindingUnbound, nil), true, func(e *repository.Event) { e.ID = 2 }},
		{"forged_scope", true, busTestBoundary(spaces.BindingUnbound, nil), true, func(e *repository.Event) { e.Bucket = "other" }},
		{"forged_type", true, busTestBoundary(spaces.BindingUnbound, nil), true, func(e *repository.Event) { e.Type = repository.EventDeleted }},
		{"forged_object", true, busTestBoundary(spaces.BindingUnbound, nil), true, func(e *repository.Event) { v := int64(8); e.ObjectID = &v }},
		{"forged_payload", true, busTestBoundary(spaces.BindingUnbound, nil), true, func(e *repository.Event) { e.Payload = map[string]string{"v": "2"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := busTestDeliverEvent()
			row := busTestDeliverEvent()
			if tc.mutate != nil {
				tc.mutate(&row)
			}
			repo := &busTestAuthorityRepo{fakeRepo: &fakeRepo{}}
			if tc.rows {
				repo.rows = []repository.Event{row}
			}
			bus := New(repo, quietLogger())
			if tc.authority {
				authority, err := NewEventAuthority(repo)
				if err != nil {
					t.Fatalf("new authority: %v", err)
				}
				bus.WithEventAuthority(authority)
			}
			if tc.boundary != nil {
				bus.WithResourceBoundary(tc.boundary)
			}
			sub, cancel := bus.Subscribe()
			defer cancel()
			bus.DeliverContext(context.Background(), want)
			expectNothing(t, sub, 50*time.Millisecond)
		})
	}
}

func TestBus_LegacyDeliverDelegatesScopedEvent(t *testing.T) {
	row := busTestDeliverEvent()
	repo := &busTestAuthorityRepo{fakeRepo: &fakeRepo{}, rows: []repository.Event{row}}
	authority, err := NewEventAuthority(repo)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	bus := New(repo, quietLogger()).WithEventAuthority(authority).
		WithResourceBoundary(busTestBoundary(spaces.BindingUnbound, nil))
	sub, cancel := bus.Subscribe()
	defer cancel()
	bus.Deliver(row)
	if got, ok := recv(t, sub, time.Second); !ok || got.ID != row.ID {
		t.Fatalf("delivered row = %+v", got)
	}
	if repo.listCalls != 1 {
		t.Fatalf("legacy Deliver rereads = %d, want 1", repo.listCalls)
	}
}

func TestPublish_ConcurrentSafe(t *testing.T) {
	// Exercise the RWMutex: many concurrent publishers + a subscriber draining.
	// Run under -race to catch data races.
	b := New(&fakeRepo{}, quietLogger())
	ch, _ := b.Subscribe()

	var received int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				atomic.AddInt64(&received, 1)
			case <-stop:
				return
			}
		}
	}()

	const writers, each = 8, 50
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				b.Publish(context.Background(), repository.Event{Key: "c", Type: repository.EventCreated})
			}
		}()
	}
	wg.Wait()
	// Give the drainer a brief moment, then stop it. We don't assert an exact
	// count (drops are allowed), only that nothing deadlocked/raced.
	time.Sleep(20 * time.Millisecond)
	close(stop)
}
