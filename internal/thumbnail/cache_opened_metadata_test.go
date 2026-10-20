package thumbnail

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestCacheOpenedAccessMetadataSharedByValue(t *testing.T) {
	identity, data := coalescingFixture(t)
	cache := NewCache(1<<20, 0)
	opener := &coalescingOpener{data: data, identity: identity, etag: etagA, entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(opener.release) })
	defer release()
	opened := OpenedSource{Identity: identity, ETag: etagA, Bound: true, UpdatedAt: time.Unix(200, 0).UTC(),
		ObjectID: 72, Backend: "local", Size: int64(len(data)), ContentType: "image/png"}
	open := func() (io.ReadCloser, OpenedSource, error) {
		rc, _, err := opener.open()
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
	assertCacheOpenedStorePolicyResult(t, leader.result, leader.err, opened, false)
	assertCacheOpenedStorePolicyResult(t, follower.result, follower.err, opened, true)
	if !bytes.Equal(leader.result.Image, follower.result.Image) || opener.opens.Load() != 1 || cache.Len() != 1 {
		t.Error("opened metadata changed sharing or resident admission")
	}
	follower.result.Opened.ObjectID, follower.result.Opened.ContentType = 99, "changed-only-in-caller-copy"
	if leader.result.Opened != opened {
		t.Error("follower's metadata mutation escaped its value copy")
	}
	assertFlightGone(t, cache, key)
	hit, err := GenerateContextWithOpenerCachedWithAdmissionResult(context.Background(), cache, nil, identity, etagA, 32, 32, open)
	if err != nil || !hit.FromCache || hit.HasOpened || hit.Opened != (OpenedSource{}) || opener.opens.Load() != 1 {
		t.Errorf("resident hit retained an opened snapshot or reopened: result=%+v err=%v opens=%d", hit, err, opener.opens.Load())
	}
}
