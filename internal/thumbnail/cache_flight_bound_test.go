package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pendingFlightCount(c *Cache) int {
	if c == nil {
		return 0
	}
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	return len(c.flights)
}

func boundTestKey(identity SourceIdentity, dimension int) CacheKey {
	return currentThumbnailCacheKey(identity, etagA, dimension, dimension)
}

func cleanupPendingFlights(c *Cache) {
	c.flightMu.Lock()
	remaining := make(map[CacheKey]*cacheFlight, len(c.flights))
	for key, flight := range c.flights {
		remaining[key] = flight
	}
	c.flightMu.Unlock()
	for key, flight := range remaining {
		finishFlight(c, key, flight, CachedGenerationResult{}, nil)
	}
}

func waitForPendingFlightCount(c *Cache, want int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pendingFlightCount(c) == want {
			return true
		}
		runtime.Gosched()
	}
	return pendingFlightCount(c) == want
}

func TestBeginFlightBoundAndOverflow(t *testing.T) {
	if maxPendingFlights != maxConcurrentDecodes {
		t.Fatalf("maxPendingFlights=%d, want maxConcurrentDecodes=%d", maxPendingFlights, maxConcurrentDecodes)
	}
	if maxPendingFlights != 4 {
		t.Fatalf("maxPendingFlights=%d, want 4", maxPendingFlights)
	}

	cache := NewCache(1<<20, 0)
	identity := testIdentity("flight-bound")
	flights := make(map[CacheKey]*cacheFlight, maxPendingFlights)
	keys := make([]CacheKey, 0, maxPendingFlights)
	for i := 0; i < maxPendingFlights; i++ {
		key := boundTestKey(identity, 16+i)
		flight, leader := beginFlight(cache, key)
		if flight == nil || !leader {
			t.Fatalf("flight %d: got flight=%p leader=%v", i, flight, leader)
		}
		keys = append(keys, key)
		flights[key] = flight
	}
	defer cleanupPendingFlights(cache)
	if got := pendingFlightCount(cache); got != maxPendingFlights {
		t.Fatalf("pending flights=%d, want %d", got, maxPendingFlights)
	}

	overflowKey := boundTestKey(identity, 64)
	if flight, leader := beginFlight(cache, overflowKey); flight != nil || leader {
		t.Fatalf("overflow begin returned flight=%p leader=%v", flight, leader)
	}
	if got := pendingFlightCount(cache); got != maxPendingFlights {
		t.Fatalf("overflow changed pending flights=%d, want %d", got, maxPendingFlights)
	}
	cache.flightMu.Lock()
	for key, want := range flights {
		if got := cache.flights[key]; got != want {
			cache.flightMu.Unlock()
			t.Fatalf("overflow replaced flight for %v: got %p want %p", key, got, want)
		}
	}
	cache.flightMu.Unlock()

	joined, leader := beginFlight(cache, keys[0])
	if joined != flights[keys[0]] || leader {
		t.Fatalf("full registry did not join existing flight: got=%p leader=%v", joined, leader)
	}
	cache.flightMu.Lock()
	joiners := joined.joiners
	cache.flightMu.Unlock()
	if joiners != 1 {
		t.Fatalf("existing flight joiners=%d, want 1", joiners)
	}
	if got := pendingFlightCount(cache); got != maxPendingFlights {
		t.Fatalf("same-key join changed pending flights=%d, want %d", got, maxPendingFlights)
	}

	data := makePNG(t, 64, 64)
	var opens atomic.Int64
	result, err := GenerateContextWithOpenerCachedWithAdmissionResult(
		context.Background(), cache, nil, identity, etagA, 64, 64,
		countingOpener3(data, etagA, &opens, identity),
	)
	if err != nil {
		t.Fatalf("overflow generation: %v", err)
	}
	if result.FromCache || !result.HasOpened || len(result.Image) == 0 {
		t.Fatalf("overflow result: fromCache=%v hasOpened=%v bytes=%d", result.FromCache, result.HasOpened, len(result.Image))
	}
	if result.Opened.Identity != identity || result.Opened.ETag != etagA {
		t.Fatalf("overflow opened metadata=%+v, want identity and etag", result.Opened)
	}
	if opens.Load() != 1 {
		t.Fatalf("overflow opener calls=%d, want 1", opens.Load())
	}
	cache.flightMu.Lock()
	_, present := cache.flights[overflowKey]
	cache.flightMu.Unlock()
	if present {
		t.Fatal("overflow generation inserted a flight")
	}
	if got := pendingFlightCount(cache); got != maxPendingFlights {
		t.Fatalf("overflow generation changed pending flights=%d, want %d", got, maxPendingFlights)
	}
}

func TestBeginFlightBypassPathsDoNotAllocate(t *testing.T) {
	complete := testIdentity("bypass")
	incomplete := complete
	incomplete.VersionID = ""
	cases := []struct {
		name  string
		cache *Cache
		id    SourceIdentity
	}{
		{name: "nil cache", cache: nil, id: complete},
		{name: "disabled cache", cache: NewCache(0, 0), id: complete},
		{name: "incomplete identity", cache: NewCache(1<<20, 0), id: incomplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flight, leader := beginFlight(tc.cache, boundTestKey(tc.id, 32))
			if flight != nil || leader {
				t.Fatalf("bypass returned flight=%p leader=%v", flight, leader)
			}
			if got := pendingFlightCount(tc.cache); got != 0 {
				t.Fatalf("bypass allocated %d flights", got)
			}
		})
	}
}

func TestCacheFlightsBoundUnderSaturatedDecodeSlots(t *testing.T) {
	cache := NewCache(1<<20, 0)
	data := makePNG(t, 32, 32)
	slotSaturate()
	defer releaseAndRecoverSlots(t)

	total := maxPendingFlights + 3
	start := make(chan struct{})
	results := make(chan error, total)
	cancels := make([]context.CancelFunc, 0, total)
	var workers sync.WaitGroup
	var opens atomic.Int64
	for i := 0; i < total; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		identity := testIdentity("saturated")
		identity.Key = "image-" + strconv.Itoa(i)
		open := countingOpener3(data, etagA, &opens, identity)
		workers.Add(1)
		go func(id SourceIdentity, opener Opener, request context.Context) {
			defer workers.Done()
			<-start
			_, _, err := GenerateContextWithOpenerCached(request, cache, id, etagA, 32, 32, opener)
			results <- err
		}(identity, open, ctx)
	}
	close(start)

	if !waitForPendingFlightCount(cache, maxPendingFlights) {
		for _, cancel := range cancels {
			cancel()
		}
		t.Fatalf("pending flights never reached bound; got %d", pendingFlightCount(cache))
	}
	if got := pendingFlightCount(cache); got > maxPendingFlights {
		t.Fatalf("pending flights=%d exceeded bound %d", got, maxPendingFlights)
	}
	if opens.Load() != 0 {
		t.Fatalf("opener calls=%d while all decode slots are held, want 0", opens.Load())
	}
	select {
	case err := <-results:
		t.Fatalf("request completed while decode slots were saturated: %v", err)
	default:
	}

	for _, cancel := range cancels {
		cancel()
	}
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled saturated callers did not finish")
	}
	for i := 0; i < total; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("caller %d error=%v, want context.Canceled", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("missing canceled caller result")
		}
	}
	if got := pendingFlightCount(cache); got != 0 {
		t.Fatalf("canceled callers left %d flights", got)
	}
}

func TestCacheGenerateSameKeyCoalescesAtFlightBound(t *testing.T) {
	cache := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	placeholderKeys := make([]CacheKey, 0, maxPendingFlights-1)
	for i := 0; i < maxPendingFlights-1; i++ {
		key := boundTestKey(identity, 80+i)
		if flight, leader := beginFlight(cache, key); flight == nil || !leader {
			t.Fatalf("placeholder %d: flight=%p leader=%v", i, flight, leader)
		}
		placeholderKeys = append(placeholderKeys, key)
	}
	defer cleanupPendingFlights(cache)

	opener := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	type callResult struct {
		result CachedGenerationResult
		err    error
	}
	leaderDone := make(chan callResult, 1)
	go func() {
		result, err := GenerateContextWithOpenerCachedWithAdmissionResult(
			context.Background(), cache, nil, identity, etagA, 32, 32, opener.open,
		)
		leaderDone <- callResult{result: result, err: err}
	}()
	waitFlightEntered(t, opener)
	key := currentThumbnailCacheKey(identity, etagA, 32, 32)
	const followers = 3
	followerDone := make(chan callResult, followers)
	for i := 0; i < followers; i++ {
		go func() {
			result, err := GenerateContextWithOpenerCachedWithAdmissionResult(
				context.Background(), cache, nil, identity, etagA, 32, 32, opener.open,
			)
			followerDone <- callResult{result: result, err: err}
		}()
	}
	waitFlightJoiners(t, cache, key, followers)
	if got := pendingFlightCount(cache); got != maxPendingFlights {
		t.Fatalf("same-key followers changed pending flights=%d, want %d", got, maxPendingFlights)
	}
	close(opener.release)

	leader := <-leaderDone
	if leader.err != nil || leader.result.FromCache || !leader.result.HasOpened {
		t.Fatalf("leader result=%+v err=%v", leader.result, leader.err)
	}
	for i := 0; i < followers; i++ {
		follower := <-followerDone
		if follower.err != nil || !follower.result.FromCache || !follower.result.HasOpened {
			t.Fatalf("follower %d result=%+v err=%v", i, follower.result, follower.err)
		}
		if !bytes.Equal(follower.result.Image, leader.result.Image) || follower.result.Opened != leader.result.Opened {
			t.Fatalf("follower %d did not receive the leader result", i)
		}
	}
	if opener.opens.Load() != 1 {
		t.Fatalf("same-key opener calls=%d, want 1", opener.opens.Load())
	}
	assertFlightGone(t, cache, key)
	if got := pendingFlightCount(cache); got != len(placeholderKeys) {
		t.Fatalf("placeholder flights=%d, want %d", got, len(placeholderKeys))
	}
}

func TestCacheGenerateLeaderCancellationWhileWaitingRemovesFlight(t *testing.T) {
	cache := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	slotSaturate()
	defer releaseAndRecoverSlots(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var opens atomic.Int64
	done := make(chan error, 1)
	go func() {
		_, _, err := GenerateContextWithOpenerCached(
			ctx, cache, identity, etagA, 32, 32,
			countingOpener3(data, etagA, &opens, identity),
		)
		done <- err
	}()
	key := currentThumbnailCacheKey(identity, etagA, 32, 32)
	if !waitForPendingFlightCount(cache, 1) {
		t.Fatalf("leader flight was not registered; got %d", pendingFlightCount(cache))
	}
	if opens.Load() != 0 {
		t.Fatalf("opener calls=%d while slots are held, want 0", opens.Load())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("leader error=%v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled leader did not finish")
	}
	assertFlightGone(t, cache, key)
	if opens.Load() != 0 {
		t.Fatalf("canceled leader opener calls=%d, want 0", opens.Load())
	}
}

func TestFinishFlightDoesNotDeleteReplacement(t *testing.T) {
	cache := NewCache(1<<20, 0)
	identity := testIdentity("replacement")
	key := boundTestKey(identity, 32)
	old := &cacheFlight{done: make(chan struct{})}
	replacement := &cacheFlight{done: make(chan struct{})}
	cache.flightMu.Lock()
	cache.flights = map[CacheKey]*cacheFlight{key: replacement}
	cache.flightMu.Unlock()

	finishFlight(cache, key, old, CachedGenerationResult{}, errors.New("old flight"))
	cache.flightMu.Lock()
	got := cache.flights[key]
	cache.flightMu.Unlock()
	if got != replacement {
		t.Fatalf("stale completion removed replacement: got %p want %p", got, replacement)
	}
	finishFlight(cache, key, replacement, CachedGenerationResult{}, nil)
	if got := pendingFlightCount(cache); got != 0 {
		t.Fatalf("replacement cleanup left %d flights", got)
	}
}
