package thumbnail

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertCacheOpenedStorePolicyResult(t *testing.T, got CachedGenerationResult, err error, opened OpenedSource, fromCache bool) {
	t.Helper()
	if err != nil || len(got.Image) == 0 || got.FromCache != fromCache {
		t.Fatalf("policy generation: err=%v bytes=%d fromCache=%v want=%v", err, len(got.Image), got.FromCache, fromCache)
	}
	if !got.HasOpened || got.Opened != opened || !got.Opened.Bound {
		t.Fatalf("storage policy lost generation proof/metadata: got=%+v want=%+v", got.Opened, opened)
	}
}

func TestCacheOpenedStorePolicyVetoAndZeroValue(t *testing.T) {
	identity, data := coalescingFixture(t)
	for _, blocked := range []bool{true, false} {
		cache := NewCache(1<<20, 0)
		opened := OpenedSource{Identity: identity, ETag: etagA, Bound: true, CacheStoreBlocked: blocked}
		var closes atomic.Int32
		opens := 0
		open := func() (io.ReadCloser, OpenedSource, error) {
			opens++
			return &recordingReadCloser{Reader: bytes.NewReader(data), closed: &closes}, opened, nil
		}
		first, err := GenerateContextWithOpenerCachedWithAdmissionResult(context.Background(), cache, nil, identity, etagA, 32, 32, open)
		assertCacheOpenedStorePolicyResult(t, first, err, opened, false)
		second, err := GenerateContextWithOpenerCachedWithAdmissionResult(context.Background(), cache, nil, identity, etagA, 32, 32, open)
		if err != nil || !bytes.Equal(first.Image, second.Image) || second.FromCache == blocked {
			t.Errorf("follow-up: blocked=%v err=%v fromCache=%v", blocked, err, second.FromCache)
		}
		wantEntries, wantOpens := 1, 1
		if blocked {
			wantEntries, wantOpens = 0, 2
			assertCacheOpenedStorePolicyResult(t, second, err, opened, false)
		}
		if cache.Len() != wantEntries || opens != wantOpens || int(closes.Load()) != wantOpens {
			t.Errorf("policy storage/lifecycle: entries=%d opens=%d closes=%d want=%d/%d", cache.Len(), opens, closes.Load(), wantEntries, wantOpens)
		}
		assertFlightGone(t, cache, currentThumbnailCacheKey(identity, etagA, 32, 32))
	}
}

type cacheOpenedStorePolicyOutcome struct {
	result CachedGenerationResult
	err    error
}

func awaitCacheOpenedStorePolicy(t *testing.T, done <-chan cacheOpenedStorePolicyOutcome) cacheOpenedStorePolicyOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("policy flight did not finish")
		return cacheOpenedStorePolicyOutcome{}
	}
}

func TestCacheOpenedStorePolicyPreservesFlightMetadataWithoutResidentStore(t *testing.T) {
	identity, data := coalescingFixture(t)
	cache := NewCache(1<<20, 0)
	opener := &coalescingOpener{data: data, identity: identity, etag: etagA, entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(opener.release) })
	defer release()
	open := func() (io.ReadCloser, OpenedSource, error) {
		rc, opened, err := opener.open()
		opened.CacheStoreBlocked = true
		return rc, opened, err
	}
	invoke := func(done chan<- cacheOpenedStorePolicyOutcome) {
		result, err := GenerateContextWithOpenerCachedWithAdmissionResult(context.Background(), cache, nil, identity, etagA, 32, 32, open)
		done <- cacheOpenedStorePolicyOutcome{result, err}
	}
	leaderDone, followerDone := make(chan cacheOpenedStorePolicyOutcome, 1), make(chan cacheOpenedStorePolicyOutcome, 1)
	go invoke(leaderDone)
	waitFlightEntered(t, opener)
	go invoke(followerDone)
	key := currentThumbnailCacheKey(identity, etagA, 32, 32)
	waitFlightJoiners(t, cache, key, 1)
	release()
	leader, follower := awaitCacheOpenedStorePolicy(t, leaderDone), awaitCacheOpenedStorePolicy(t, followerDone)
	opened := OpenedSource{Identity: identity, ETag: etagA, Bound: true, CacheStoreBlocked: true}
	assertCacheOpenedStorePolicyResult(t, leader.result, leader.err, opened, false)
	assertCacheOpenedStorePolicyResult(t, follower.result, follower.err, opened, true)
	if !bytes.Equal(leader.result.Image, follower.result.Image) || opener.opens.Load() != 1 || cache.Len() != 0 || cache.Bytes() != 0 {
		t.Errorf("flight policy changed sharing/stored bytes: opens=%d entries=%d bytes=%d", opener.opens.Load(), cache.Len(), cache.Bytes())
	}
	assertFlightGone(t, cache, key)
	retry, err := GenerateContextWithOpenerCachedWithAdmissionResult(context.Background(), cache, nil, identity, etagA, 32, 32, open)
	assertCacheOpenedStorePolicyResult(t, retry, err, opened, false)
	if opener.opens.Load() != 2 || cache.Len() != 0 {
		t.Errorf("independent request reused forbidden resident result: opens=%d entries=%d", opener.opens.Load(), cache.Len())
	}
}
