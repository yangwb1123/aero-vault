package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitFlightPrefersCompletedErrorWhenContextReady(t *testing.T) {
	sentinel := errors.New("leader failed")
	flight := &cacheFlight{done: make(chan struct{}), err: sentinel}
	close(flight.done)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	img, from, err := waitFlight(ctx, flight)
	if !errors.Is(err, sentinel) {
		t.Fatalf("waitFlight error=%v, want sentinel", err)
	}
	if from {
		t.Fatal("waitFlight reported fromCache on completed error")
	}
	if img != nil {
		t.Fatalf("waitFlight returned image on completed error: %q", img)
	}
}

func TestCacheGenerateConcurrentDifferentKeysDoNotShareFlight(t *testing.T) {
	c := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	openerA := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	openerB := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	type result struct {
		img []byte
		err error
	}
	results := make(chan result, 2)
	go func() {
		img, _, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, openerA.open)
		results <- result{img: img, err: err}
	}()
	go func() {
		img, _, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 48, 48, openerB.open)
		results <- result{img: img, err: err}
	}()
	waitFlightEntered(t, openerA)
	waitFlightEntered(t, openerB)
	c.flightMu.Lock()
	flights := len(c.flights)
	c.flightMu.Unlock()
	if flights != 2 {
		t.Fatalf("in-flight keys=%d, want 2", flights)
	}
	close(openerA.release)
	close(openerB.release)
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			if got.err != nil || len(got.img) == 0 {
				t.Fatalf("different-key caller %d: len=%d err=%v", i, len(got.img), got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("different-key caller did not finish")
		}
	}
	if openerA.opens.Load() != 1 || openerB.opens.Load() != 1 {
		t.Fatalf("different-key opener counts=%d/%d, want 1/1", openerA.opens.Load(), openerB.opens.Load())
	}
	if c.Len() != 2 {
		t.Fatalf("different-key cache entries=%d, want 2", c.Len())
	}
	assertFlightGone(t, c, currentThumbnailCacheKey(identity, etagA, 32, 32))
	assertFlightGone(t, c, currentThumbnailCacheKey(identity, etagA, 48, 48))
}

func TestCacheGenerateConcurrentSameKeyDifferentCachesIndependent(t *testing.T) {
	cacheA := NewCache(1<<20, 0)
	cacheB := NewCache(1<<20, 0)
	identity, data := coalescingFixture(t)
	openerA := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	openerB := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	type result struct {
		img []byte
		err error
	}
	results := make(chan result, 2)
	go func() {
		img, _, err := GenerateContextWithOpenerCached(context.Background(), cacheA, identity, etagA, 32, 32, openerA.open)
		results <- result{img: img, err: err}
	}()
	go func() {
		img, _, err := GenerateContextWithOpenerCached(context.Background(), cacheB, identity, etagA, 32, 32, openerB.open)
		results <- result{img: img, err: err}
	}()
	waitFlightEntered(t, openerA)
	waitFlightEntered(t, openerB)
	cacheA.flightMu.Lock()
	flightsA := len(cacheA.flights)
	cacheA.flightMu.Unlock()
	cacheB.flightMu.Lock()
	flightsB := len(cacheB.flights)
	cacheB.flightMu.Unlock()
	if flightsA != 1 || flightsB != 1 {
		t.Fatalf("same-key different-caches flights=%d/%d, want 1/1", flightsA, flightsB)
	}
	close(openerA.release)
	close(openerB.release)
	var first []byte
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			if got.err != nil || len(got.img) == 0 {
				t.Fatalf("different-cache caller %d: len=%d err=%v", i, len(got.img), got.err)
			}
			if first == nil {
				first = got.img
			} else if !bytes.Equal(first, got.img) {
				t.Fatalf("different-cache caller %d received different bytes", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("different-cache caller did not finish")
		}
	}
	if openerA.opens.Load() != 1 || openerB.opens.Load() != 1 {
		t.Fatalf("same-key different-caches opener counts=%d/%d, want 1/1", openerA.opens.Load(), openerB.opens.Load())
	}
	if cacheA.Len() != 1 || cacheB.Len() != 1 {
		t.Fatalf("same-key different-caches cache entries=%d/%d, want 1/1", cacheA.Len(), cacheB.Len())
	}
	key := currentThumbnailCacheKey(identity, etagA, 32, 32)
	assertFlightGone(t, cacheA, key)
	assertFlightGone(t, cacheB, key)
}

func TestCacheGenerateConcurrentSameKeySuccessWithoutStoreRetriesLater(t *testing.T) {
	c := NewCache(1, 0)
	identity, data := coalescingFixture(t)
	opener := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	const callers = 4
	type result struct {
		img []byte
		err error
	}
	start := make(chan struct{})
	results := make(chan result, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			img, _, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, opener.open)
			results <- result{img: img, err: err}
		}()
	}
	close(start)
	waitFlightEntered(t, opener)
	key := currentThumbnailCacheKey(identity, etagA, 32, 32)
	waitFlightJoiners(t, c, key, callers-1)
	close(opener.release)
	var first []byte
	for i := 0; i < callers; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("no-store caller %d error=%v", i, got.err)
			}
			if first == nil {
				first = got.img
			} else if !bytes.Equal(first, got.img) {
				t.Fatalf("no-store caller %d received different bytes", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no-store caller did not finish")
		}
	}
	if opener.opens.Load() != 1 {
		t.Fatalf("no-store opener calls=%d, want 1", opener.opens.Load())
	}
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatalf("no-store cache state len=%d bytes=%d, want 0/0", c.Len(), c.Bytes())
	}
	assertFlightGone(t, c, key)
	retry := &coalescingOpener{
		data: data, identity: identity, etag: etagA,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	close(retry.release)
	img, from, err := GenerateContextWithOpenerCached(context.Background(), c, identity, etagA, 32, 32, retry.open)
	if err != nil || from || !bytes.Equal(first, img) {
		t.Fatalf("independent retry: len=%d from=%v err=%v equal=%v", len(img), from, err, bytes.Equal(first, img))
	}
	if retry.opens.Load() != 1 {
		t.Fatalf("independent retry opens=%d, want 1", retry.opens.Load())
	}
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatalf("independent retry cached store-refused result: len=%d bytes=%d", c.Len(), c.Bytes())
	}
}
