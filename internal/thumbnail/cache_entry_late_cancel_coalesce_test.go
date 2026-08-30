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

type cachedCallResult struct {
	img       []byte
	fromCache bool
	err       error
}

func TestCachedGenerationLateCancelBeforeStoreCoalesces(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{name: "canceled", want: context.Canceled},
		{name: "deadline", want: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, seededKey, seededPayload, beforeLen, beforeBytes := seedLateCancelCache(t)
			identity := testIdentity("tenant-a")
			targetKey := lateCancelCacheKey(identity, etagA, 32, 32)
			data := makePNG(t, 64, 64)
			leaderCtx := newLateCancelContext()
			entered := make(chan struct{})
			release := make(chan struct{})
			var enteredOnce sync.Once
			var opens atomic.Int32
			open := func() (io.ReadCloser, OpenedSource, error) {
				opens.Add(1)
				enteredOnce.Do(func() { close(entered) })
				<-release
				return &closeFailingSource{
					Reader: bytes.NewReader(data),
					onClose: func() {
						leaderCtx.trip(tc.want)
					},
				}, OpenedSource{Identity: identity, ETag: etagA, Bound: true}, nil
			}
			leaderDone := make(chan cachedCallResult, 1)
			joinerDone := make(chan cachedCallResult, 1)
			go func() {
				img, fromCache, err := GenerateContextWithOpenerCached(leaderCtx, cache, identity, etagA, 32, 32, open)
				leaderDone <- cachedCallResult{img: img, fromCache: fromCache, err: err}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("leader did not enter opener")
			}
			go func() {
				img, fromCache, err := GenerateContextWithOpenerCached(context.Background(), cache, identity, etagA, 32, 32, open)
				joinerDone <- cachedCallResult{img: img, fromCache: fromCache, err: err}
			}()
			waitFlightJoiners(t, cache, targetKey, 1)
			close(release)
			assertLateCancelCallResult(t, <-leaderDone, tc.want)
			assertLateCancelCallResult(t, <-joinerDone, tc.want)
			if opens.Load() != 1 {
				t.Fatalf("opener calls=%d, want 1", opens.Load())
			}
			if cache.Len() != beforeLen || cache.Bytes() != beforeBytes {
				t.Fatalf("cache changed: len=%d/%d bytes=%d/%d", cache.Len(), beforeLen, cache.Bytes(), beforeBytes)
			}
			seeded, ok := cacheEntryBytes(cache, seededKey)
			if !ok || !bytes.Equal(seeded, seededPayload) {
				t.Fatalf("seeded entry changed: present=%v payload=%q", ok, seeded)
			}
			if _, ok := cacheEntryBytes(cache, targetKey); ok {
				t.Fatal("late-canceled coalesced miss stored the target cache entry")
			}
			assertFlightGone(t, cache, targetKey)
			assertSlotsReleased(t)
		})
	}
}

func assertLateCancelCallResult(t *testing.T, got cachedCallResult, want error) {
	t.Helper()
	if got.err != want {
		t.Fatalf("err = %v, want exact %v", got.err, want)
	}
	if !errors.Is(got.err, want) {
		t.Fatalf("errors.Is(err, %v) = false, want true", want)
	}
	if got.fromCache {
		t.Fatal("fromCache = true, want false")
	}
	if len(got.img) != 0 {
		t.Fatalf("len(img) = %d, want 0", len(got.img))
	}
}
