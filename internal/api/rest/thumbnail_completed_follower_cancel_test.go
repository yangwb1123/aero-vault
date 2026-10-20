package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// Pause only the request goroutine at the flight select's Done evaluation,
// not database/sql's asynchronous cancellation observers. The original Done
// channel and Err are unchanged: this forces a schedulable completed+cancelled
// interleaving without masking cancellation or changing flight selection rules.
// CallersFrames includes inlined frames, so the hook also works in normal builds.
type thumbnailFollowerPauseContext struct {
	context.Context
	reached chan struct{}
	resume  chan struct{}
	once    sync.Once
	release sync.Once
}

func (c *thumbnailFollowerPauseContext) Done() <-chan struct{} {
	var pcs [24]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, "/thumbnail.waitFlightResult") {
			c.once.Do(func() { close(c.reached) })
			<-c.resume
			break
		}
		if !more {
			break
		}
	}
	return c.Context.Done()
}

func (c *thumbnailFollowerPauseContext) unblock() { c.release.Do(func() { close(c.resume) }) }

type thumbnailFollowerWrites struct {
	http.ResponseWriter
	status int
	bytes  int
	body   []byte
}

func (w *thumbnailFollowerWrites) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *thumbnailFollowerWrites) Write(body []byte) (int, error) {
	w.bytes += len(body)
	w.body = append(w.body, body...)
	return w.ResponseWriter.Write(body)
}

type thumbnailFollowerFinish struct {
	status int
	bytes  int
	header http.Header
	body   []byte
}

type thumbnailFollowerCancelFixture struct {
	*thumbnailAccessSourceFixture
	pause     *thumbnailFollowerPauseContext
	installed chan struct{}
	finished  chan thumbnailFollowerFinish
}

func newThumbnailFollowerCancelFixture(t *testing.T, timeout time.Duration) *thumbnailFollowerCancelFixture {
	t.Helper()
	base := newThumbnailAccessSourceFixture(t)
	inner := base.web.Config.Handler
	base.web.Close()
	f := &thumbnailFollowerCancelFixture{thumbnailAccessSourceFixture: base,
		pause:     &thumbnailFollowerPauseContext{reached: make(chan struct{}), resume: make(chan struct{})},
		installed: make(chan struct{}), finished: make(chan thumbnailFollowerFinish, 1)}
	base.web = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") != "cancel-follower" {
			inner.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		f.pause.Context = ctx
		close(f.installed)
		tracked := &thumbnailFollowerWrites{ResponseWriter: w}
		inner.ServeHTTP(tracked, r.WithContext(f.pause))
		f.finished <- thumbnailFollowerFinish{status: tracked.status, bytes: tracked.bytes, header: w.Header().Clone(), body: tracked.body}
	}))
	t.Cleanup(f.pause.unblock)
	return f
}

func (f *thumbnailFollowerCancelFixture) waitJoined(t *testing.T) {
	t.Helper()
	waitThumbnailAccessSignal(t, f.installed)
	waitThumbnailAccessSignal(t, f.pause.reached)
	_, misses, _, _ := f.cache.Stats()
	if misses != 2 || f.store.opens.Load() != 1 {
		t.Fatalf("follower did not join one blocked flight: misses=%d opens=%d", misses, f.store.opens.Load())
	}
}

func (f *thumbnailFollowerCancelFixture) finish(t *testing.T) thumbnailFollowerFinish {
	t.Helper()
	select {
	case result := <-f.finished:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("follower handler did not finish")
		return thumbnailFollowerFinish{}
	}
}

func assertThumbnailFollowerCancelLifecycle(t *testing.T, f *thumbnailFollowerCancelFixture) {
	t.Helper()
	if f.sink.count(repository.EventAccessed) != 1 {
		t.Errorf("cancelled follower emitted an access attempt: total=%d want leader only", f.sink.count(repository.EventAccessed))
	}
	assertThumbnailAccessSourceEvent(t, f.sink, "thumbnail-leader", f.current)
	assertThumbnailAccessSourceLifecycle(t, f.thumbnailAccessSourceFixture, f.current, 1, 0)
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	if f.repo.calls["thumbnail-leader"] != 2 || f.repo.calls["cancel-follower"] != 1 {
		t.Errorf("cancellation changed source observation counts: %v", f.repo.calls)
	}
}

func assertThumbnailFollowerNoSuccessHeaders(t *testing.T, header http.Header) {
	t.Helper()
	for _, name := range []string{"ETag", "Last-Modified", "X-Version-Id", "Cache-Control", "Content-Length", "Content-Range"} {
		if header.Get(name) != "" {
			t.Errorf("cancelled follower certified %s=%q", name, header.Get(name))
		}
	}
}

func TestThumbnailCompletedFollowerClientCancelDoesNotRespondOrEmit(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, selector := range []string{"?representation=thumbnail&w=16&h=16", "/thumbnail?w=16&h=16"} {
			t.Run(method+selector, func(t *testing.T) {
				f := newThumbnailFollowerCancelFixture(t, 0)
				path := "/v1/files/" + f.current.Key + selector
				leader := f.start(method, path, "thumbnail-leader", context.Background(), nil)
				waitThumbnailAccessSignal(t, f.entered)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				follower := f.start(method, path, "cancel-follower", ctx, nil)
				f.waitJoined(t)
				cancel()
				assertThumbnailFollowerClientCancelled(t, follower)
				waitThumbnailAccessSignal(t, f.pause.Context.Done())
				f.unblock()
				assertThumbnailAccessSourceResponse(t, f.thumbnailAccessSourceFixture, collectThumbnailAccessResponse(t, leader), method)
				before := snapshotThumbCounters(t)
				f.pause.unblock()
				result := f.finish(t)
				assertThumbnailFollowerSilent(t, result)
				assertThumbnailFollowerCancelLifecycle(t, f)
				assertThumbDeltas(t, before, "", 0)
			})
		}
	}
}

func assertThumbnailFollowerSilent(t *testing.T, result thumbnailFollowerFinish) {
	t.Helper()
	if result.status != 0 || result.bytes != 0 || result.header.Get("Content-Type") != "" {
		t.Errorf("cancelled completed follower wrote a response: status=%d bytes=%d headers=%v", result.status, result.bytes, result.header)
	}
	assertThumbnailFollowerNoSuccessHeaders(t, result.header)
}

func assertThumbnailFollowerClientCancelled(t *testing.T, result <-chan thumbnailAccessHTTPResult) {
	t.Helper()
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("client cancellation did not propagate: %v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled HTTP client did not finish")
	}
}
