package thumbnail

import (
	"bytes"
	"context"
	"testing"
)

func TestGenerateContextWithOpenerCachedWithAdmissionResultMetadata(t *testing.T) {
	identity, data := coalescingFixture(t)
	admission := NewDecodeAdmission(1)

	t.Run("miss reports opened metadata", func(t *testing.T) {
		cache := NewCache(1<<20, 0)
		opener := &coalescingOpener{
			data: data, identity: identity, etag: etagA,
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		close(opener.release)

		got, err := GenerateContextWithOpenerCachedWithAdmissionResult(
			context.Background(), cache, admission, identity, etagA, 32, 32, opener.open,
		)
		if err != nil {
			t.Fatalf("miss: %v", err)
		}
		if got.FromCache {
			t.Fatal("miss must not report FromCache")
		}
		if !got.HasOpened {
			t.Fatal("miss must report opened metadata")
		}
		if got.Opened.Identity != identity {
			t.Fatalf("miss opened identity = %+v, want %+v", got.Opened.Identity, identity)
		}
		if got.Opened.ETag != etagA {
			t.Fatalf("miss opened etag = %q, want %q", got.Opened.ETag, etagA)
		}
		if len(got.Image) == 0 {
			t.Fatal("miss returned empty image")
		}
	})

	t.Run("resident hit omits opened metadata", func(t *testing.T) {
		cache := NewCache(1<<20, 0)
		warm := &coalescingOpener{
			data: data, identity: identity, etag: etagA,
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		close(warm.release)
		miss, err := GenerateContextWithOpenerCachedWithAdmissionResult(
			context.Background(), cache, admission, identity, etagA, 32, 32, warm.open,
		)
		if err != nil {
			t.Fatalf("warm miss: %v", err)
		}

		resident := &coalescingOpener{
			data: data, identity: identity, etag: etagA,
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		close(resident.release)
		hit, err := GenerateContextWithOpenerCachedWithAdmissionResult(
			context.Background(), cache, admission, identity, etagA, 32, 32, resident.open,
		)
		if err != nil {
			t.Fatalf("resident hit: %v", err)
		}
		if !hit.FromCache {
			t.Fatal("resident hit must report FromCache")
		}
		if hit.HasOpened {
			t.Fatal("resident hit must not report opened metadata")
		}
		if hit.Opened != (OpenedSource{}) {
			t.Fatalf("resident hit opened metadata = %+v, want zero value", hit.Opened)
		}
		if !bytes.Equal(hit.Image, miss.Image) {
			t.Fatal("resident hit bytes differ from warm miss bytes")
		}
		if resident.opens.Load() != 0 {
			t.Fatalf("resident hit reopened source: opens=%d", resident.opens.Load())
		}
	})

	t.Run("coalesced follower preserves leader opened metadata", func(t *testing.T) {
		cache := NewCache(1<<20, 0)
		opener := &coalescingOpener{
			data: data, identity: identity, etag: etagA,
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		type result struct {
			got CachedGenerationResult
			err error
		}
		leaderDone := make(chan result, 1)
		go func() {
			got, err := GenerateContextWithOpenerCachedWithAdmissionResult(
				context.Background(), cache, admission, identity, etagA, 32, 32, opener.open,
			)
			leaderDone <- result{got: got, err: err}
		}()
		waitFlightEntered(t, opener)
		key := currentThumbnailCacheKey(identity, etagA, 32, 32)
		followerDone := make(chan result, 1)
		go func() {
			got, err := GenerateContextWithOpenerCachedWithAdmissionResult(
				context.Background(), cache, admission, identity, etagA, 32, 32, opener.open,
			)
			followerDone <- result{got: got, err: err}
		}()
		waitFlightJoiners(t, cache, key, 1)
		close(opener.release)

		leader := <-leaderDone
		if leader.err != nil {
			t.Fatalf("leader: %v", leader.err)
		}
		follower := <-followerDone
		if follower.err != nil {
			t.Fatalf("follower: %v", follower.err)
		}
		if leader.got.FromCache {
			t.Fatal("leader miss must not report FromCache")
		}
		if !leader.got.HasOpened {
			t.Fatal("leader miss must report opened metadata")
		}
		if !follower.got.FromCache {
			t.Fatal("coalesced follower must report FromCache")
		}
		if !follower.got.HasOpened {
			t.Fatal("coalesced follower must preserve opened metadata")
		}
		if follower.got.Opened != leader.got.Opened {
			t.Fatalf("follower opened metadata = %+v, want %+v", follower.got.Opened, leader.got.Opened)
		}
		if follower.got.Opened.Identity != identity {
			t.Fatalf("follower opened identity = %+v, want %+v", follower.got.Opened.Identity, identity)
		}
		if follower.got.Opened.ETag != etagA {
			t.Fatalf("follower opened etag = %q, want %q", follower.got.Opened.ETag, etagA)
		}
		if !bytes.Equal(follower.got.Image, leader.got.Image) {
			t.Fatal("coalesced follower bytes differ from leader bytes")
		}
		assertFlightGone(t, cache, key)
	})
}
