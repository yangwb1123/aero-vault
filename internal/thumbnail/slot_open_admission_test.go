package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// postAdmissionContext makes the admission-to-opener handoff deterministic.
// It reports a live context through all admission checks, pauses at the last
// successful admission check, and only reports the selected error at the next
// check performed by generateContextWithAdmission.
type postAdmissionContext struct {
	context.Context
	done    chan struct{}
	ready   chan struct{}
	unblock chan struct{}
	target  int
	extra   int

	mu        sync.Mutex
	err       error
	calls     int
	readyOnce sync.Once
	doneOnce  sync.Once
}

func newPostAdmissionContext(target, extra int) *postAdmissionContext {
	return &postAdmissionContext{
		Context: context.Background(), done: make(chan struct{}),
		ready: make(chan struct{}), unblock: make(chan struct{}),
		target: target, extra: extra,
	}
}

func (c *postAdmissionContext) Done() <-chan struct{} { return c.done }

func (c *postAdmissionContext) Err() error {
	c.mu.Lock()
	c.calls++
	call := c.calls
	err := c.err
	c.mu.Unlock()
	if call == c.target {
		c.readyOnce.Do(func() { close(c.ready) })
		<-c.unblock
		return nil
	}
	if call <= c.target+c.extra {
		return nil
	}
	c.doneOnce.Do(func() { close(c.done) })
	return err
}

func (c *postAdmissionContext) cancel(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	close(c.unblock)
}

func TestPostAdmissionCancellationNeverOpensOrLeaks(t *testing.T) {
	cases := []struct {
		name      string
		admission *DecodeAdmission
		want      error
		target    int
		extra     int
	}{
		{name: "global-canceled", want: context.Canceled, target: 1},
		{name: "global-deadline", want: context.DeadlineExceeded, target: 1},
		{name: "tenant-canceled", admission: NewDecodeAdmission(1), want: context.Canceled, target: 2, extra: 1},
		{name: "tenant-deadline", admission: NewDecodeAdmission(1), want: context.DeadlineExceeded, target: 2, extra: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newPostAdmissionContext(tc.target, tc.extra)
			var opens atomic.Int64
			open := func() (io.ReadCloser, OpenedSource, error) {
				opens.Add(1)
				return io.NopCloser(bytes.NewReader(makePNG(t, 8, 8))), OpenedSource{}, nil
			}
			result := make(chan error, 1)
			go func() {
				_, _, err := generateContextWithAdmission(ctx, 16, 16, tc.admission, "tenant-a", open)
				result <- err
			}()

			select {
			case <-ctx.ready:
			case <-time.After(5 * time.Second):
				t.Fatal("admission did not reach the controlled handoff")
			}
			ctx.cancel(tc.want)
			select {
			case err := <-result:
				if err != tc.want || !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want bare %v", err, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled handoff did not return")
			}
			if got := opens.Load(); got != 0 {
				t.Fatalf("opener invoked %d times, want 0", got)
			}
			assertSlotsReleased(t)
			if tc.admission == nil {
				return
			}
			tc.admission.mu.Lock()
			_, present := tc.admission.states["tenant-a"]
			tc.admission.mu.Unlock()
			if present {
				t.Fatal("tenant admission state remained after canceled handoff")
			}
			release, err := tc.admission.Acquire(context.Background(), "tenant-a")
			if err != nil {
				t.Fatalf("subsequent tenant acquire: %v", err)
			}
			release()
			assertSlotsReleased(t)
		})
	}
}
