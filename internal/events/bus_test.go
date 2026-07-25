package events

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
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
