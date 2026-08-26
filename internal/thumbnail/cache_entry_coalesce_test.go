package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type coalescingOpener struct {
	data       []byte
	identity   SourceIdentity
	etag       string
	entered    chan struct{}
	release    chan struct{}
	enteredOne sync.Once
	opens      atomic.Int64
	err        error
	panicValue any
}

func (o *coalescingOpener) open() (io.ReadCloser, OpenedSource, error) {
	o.opens.Add(1)
	o.enteredOne.Do(func() { close(o.entered) })
	<-o.release
	if o.panicValue != nil {
		panic(o.panicValue)
	}
	if o.err != nil {
		return nil, OpenedSource{}, o.err
	}
	return io.NopCloser(bytes.NewReader(o.data)), OpenedSource{
		Identity: o.identity,
		ETag:     o.etag,
		Bound:    true,
	}, nil
}

func waitFlightEntered(t *testing.T, opener *coalescingOpener) {
	t.Helper()
	select {
	case <-opener.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("coalesced leader did not enter opener")
	}
}

func waitFlightJoiners(t *testing.T, c *Cache, key CacheKey, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		c.flightMu.Lock()
		flight := c.flights[key]
		got := 0
		if flight != nil {
			got = flight.joiners
		}
		c.flightMu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("coalesced joiners=%d, want at least %d", got, want)
		default:
			runtime.Gosched()
		}
	}
}

func assertFlightGone(t *testing.T, c *Cache, key CacheKey) {
	t.Helper()
	c.flightMu.Lock()
	_, present := c.flights[key]
	c.flightMu.Unlock()
	if present {
		t.Fatalf("flight for %v remains after completion", key)
	}
}

func coalescingFixture(t *testing.T) (SourceIdentity, []byte) {
	t.Helper()
	identity := testIdentity("t1")
	return identity, makePNG(t, 64, 64)
}

func TestCacheGenerateConcurrentSameKeySuccessCoalesces(t *testing.T) {
	c := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	opener := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	const callers = 8
	type result struct {
		img  []byte
		from bool
		err  error
	}
	start := make(chan struct{})
	done := make(chan result, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			img, from, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, opener.open)
			done <- result{img, from, err}
		}()
	}
	close(start)
	waitFlightEntered(t, opener)
	key := CacheKey{Identity: identity, SourceETag: etagA, EffW: 32, EffH: 32, Version: CacheKeyVersion}
	waitFlightJoiners(t, c, key, callers-1)
	close(opener.release)
	var first []byte
	for i := 0; i < callers; i++ {
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("caller %d: %v", i, got.err)
			}
			if first == nil {
				first = got.img
			} else if !bytes.Equal(first, got.img) {
				t.Fatalf("caller %d received different bytes", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("coalesced success caller did not finish")
		}
	}
	if opener.opens.Load() != 1 {
		t.Fatalf("opener calls=%d, want 1", opener.opens.Load())
	}
	if c.Len() != 1 {
		t.Fatalf("cache entries=%d, want 1", c.Len())
	}
	assertFlightGone(t, c, key)
	got, from, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, opener.open)
	if err != nil || !from || !bytes.Equal(first, got) {
		t.Fatalf("follow-up hit: err=%v from=%v bytes_equal=%v", err, from, bytes.Equal(first, got))
	}
	if opener.opens.Load() != 1 {
		t.Fatalf("follow-up reopened source: opens=%d", opener.opens.Load())
	}
}

func TestCacheGenerateConcurrentSameKeyFailureCoalesces(t *testing.T) {
	c := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	sentinel := errors.New("coalesced opener failure")
	opener := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}), err: sentinel,
	}
	const callers = 5
	start := make(chan struct{})
	done := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			_, _, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, opener.open)
			done <- err
		}()
	}
	close(start)
	waitFlightEntered(t, opener)
	key := CacheKey{Identity: identity, SourceETag: etagA, EffW: 32, EffH: 32, Version: CacheKeyVersion}
	waitFlightJoiners(t, c, key, callers-1)
	close(opener.release)
	for i := 0; i < callers; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, sentinel) {
				t.Fatalf("caller %d error=%v, want sentinel", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("coalesced failure caller did not finish")
		}
	}
	if opener.opens.Load() != 1 || c.Len() != 0 {
		t.Fatalf("failure opener calls=%d cache_len=%d, want 1/0", opener.opens.Load(), c.Len())
	}
	assertFlightGone(t, c, key)
	retry := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	close(retry.release)
	if _, from, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, retry.open); err != nil || from {
		t.Fatalf("independent retry: err=%v fromCache=%v", err, from)
	}
	if retry.opens.Load() != 1 || c.Len() != 1 {
		t.Fatalf("independent retry opener calls=%d cache_len=%d, want 1/1", retry.opens.Load(), c.Len())
	}
}

func TestCacheGenerateJoinedCallerCancellation(t *testing.T) {
	c := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	opener := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, opener.open)
		leaderDone <- err
	}()
	waitFlightEntered(t, opener)
	key := CacheKey{Identity: identity, SourceETag: etagA, EffW: 32, EffH: 32, Version: CacheKeyVersion}
	joinCtx, cancel := context.WithCancel(context.Background())
	joinDone := make(chan error, 1)
	go func() {
		_, _, err := GenerateContextWithOpenerCached(joinCtx, c, identity, etagA, 32, 32, opener.open)
		joinDone <- err
	}()
	waitFlightJoiners(t, c, key, 1)
	cancel()
	select {
	case err := <-joinDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("joiner error=%v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled joiner did not return")
	}
	select {
	case err := <-leaderDone:
		t.Fatalf("leader finished before release: %v", err)
	default:
	}
	close(opener.release)
	select {
	case err := <-leaderDone:
		if err != nil {
			t.Fatalf("leader error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader did not finish after release")
	}
	if opener.opens.Load() != 1 || c.Len() != 1 {
		t.Fatalf("leader opener calls=%d cache_len=%d, want 1/1", opener.opens.Load(), c.Len())
	}
	assertFlightGone(t, c, key)
}

func TestCacheGenerateLeaderPanicCleansFlight(t *testing.T) {
	c := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	panicValue := "leader panic"
	opener := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}), panicValue: panicValue,
	}
	key := CacheKey{Identity: identity, SourceETag: etagA, EffW: 32, EffH: 32, Version: CacheKeyVersion}
	leaderDone := make(chan any, 1)
	go func() {
		defer func() { leaderDone <- recover() }()
		_, _, _ = GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, opener.open)
	}()
	waitFlightEntered(t, opener)
	joinDone := make(chan error, 1)
	go func() {
		_, _, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, opener.open)
		joinDone <- err
	}()
	waitFlightJoiners(t, c, key, 1)
	close(opener.release)
	select {
	case got := <-leaderDone:
		if !reflect.DeepEqual(got, panicValue) {
			t.Fatalf("leader recovered=%v, want %v", got, panicValue)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("panicking leader did not return")
	}
	select {
	case err := <-joinDone:
		if !errors.Is(err, errCoalescedLeaderPanic) {
			t.Fatalf("joiner error=%v, want panic sentinel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("joiner remained blocked after leader panic")
	}
	assertFlightGone(t, c, key)
	if c.Len() != 0 {
		t.Fatalf("panic populated cache: len=%d", c.Len())
	}
}

func TestWaitFlightPrefersCompletedResultWhenContextReady(t *testing.T) {
	flight := &cacheFlight{
		done:   make(chan struct{}),
		result: CachedGenerationResult{Image: []byte("complete")},
	}
	close(flight.done)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	img, from, err := waitFlight(ctx, flight)
	if err != nil || !from || !bytes.Equal(img, []byte("complete")) {
		t.Fatalf("completed success result: img=%q from=%v err=%v", img, from, err)
	}
}
