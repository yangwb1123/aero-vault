package rest

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestThumbnailCompletedFollowerLiveRequestKeepsOpenedSource(t *testing.T) {
	f := newThumbnailFollowerCancelFixture(t, 0)
	path := "/v1/files/" + f.current.Key + "?representation=thumbnail&w=16&h=16"
	leader := f.start(http.MethodGet, path, "thumbnail-leader", context.Background(), nil)
	waitThumbnailAccessSignal(t, f.entered)
	follower := f.start(http.MethodGet, path, "cancel-follower", context.Background(), nil)
	f.waitJoined(t)
	f.unblock()
	assertThumbnailAccessSourceResponse(t, f.thumbnailAccessSourceFixture, collectThumbnailAccessResponse(t, leader), http.MethodGet)
	before := snapshotThumbCounters(t)
	f.pause.unblock()
	assertThumbnailAccessSourceResponse(t, f.thumbnailAccessSourceFixture, collectThumbnailAccessResponse(t, follower), http.MethodGet)
	result := f.finish(t)
	if result.status != http.StatusOK || result.bytes != len(f.jpeg) || f.sink.count(repository.EventAccessed) != 2 {
		t.Errorf("live completed follower changed: status=%d bytes=%d access=%d", result.status, result.bytes, f.sink.count(repository.EventAccessed))
	}
	assertThumbnailAccessSourceEvent(t, f.sink, "thumbnail-leader", f.current)
	assertThumbnailAccessSourceEvent(t, f.sink, "cancel-follower", f.current)
	assertThumbnailAccessSourceLifecycle(t, f.thumbnailAccessSourceFixture, f.current, 1, 0)
	assertThumbDeltas(t, before, "", 1)
}

func TestThumbnailCompletedFollowerPendingCancellationDoesNotWaitForLeader(t *testing.T) {
	f := newThumbnailFollowerCancelFixture(t, 0)
	path := "/v1/files/" + f.current.Key + "/thumbnail?w=16&h=16"
	leader := f.start(http.MethodGet, path, "thumbnail-leader", context.Background(), nil)
	waitThumbnailAccessSignal(t, f.entered)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	follower := f.start(http.MethodGet, path, "cancel-follower", ctx, nil)
	f.waitJoined(t)
	cancel()
	assertThumbnailFollowerClientCancelled(t, follower)
	waitThumbnailAccessSignal(t, f.pause.Context.Done())
	before := snapshotThumbCounters(t)
	f.pause.unblock()
	assertThumbnailFollowerSilent(t, f.finish(t))
	assertThumbDeltas(t, before, "", 0)
	if f.store.closes.Load() != 0 || f.sink.count(repository.EventAccessed) != 1 {
		t.Errorf("pending cancellation closed/shared leader: closes=%d events=%d", f.store.closes.Load(), f.sink.count(repository.EventAccessed))
	}
	f.unblock()
	assertThumbnailAccessSourceResponse(t, f.thumbnailAccessSourceFixture, collectThumbnailAccessResponse(t, leader), http.MethodGet)
	assertThumbnailFollowerCancelLifecycle(t, f)
}

func TestThumbnailCompletedFollowerCancelKeepsLeaderCache(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		runThumbnailCompletedFollowerLeaderCache(t, pinned)
	}
}

func runThumbnailCompletedFollowerLeaderCache(t *testing.T, pinned bool) {
	t.Helper()
	f := newThumbnailFollowerCancelFixture(t, 0)
	f.repo.older = f.current
	path := "/v1/files/" + f.current.Key + "?representation=thumbnail&w=16&h=16"
	if pinned {
		path += "&version=" + f.current.VersionID
	}
	leader := f.start(http.MethodGet, path, "thumbnail-leader", context.Background(), nil)
	waitThumbnailAccessSignal(t, f.entered)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	follower := f.start(http.MethodGet, path, "cancel-follower", ctx, nil)
	f.waitJoined(t)
	cancel()
	assertThumbnailFollowerClientCancelled(t, follower)
	waitThumbnailAccessSignal(t, f.pause.Context.Done())
	f.unblock()
	assertThumbnailAccessSourceResponse(t, f.thumbnailAccessSourceFixture, collectThumbnailAccessResponse(t, leader), http.MethodGet)
	before := snapshotThumbCounters(t)
	f.pause.unblock()
	assertThumbnailFollowerSilent(t, f.finish(t))
	assertThumbDeltas(t, before, "", 0)
	assertThumbnailAccessSourceLifecycle(t, f.thumbnailAccessSourceFixture, f.current, 1, 1)
	if f.sink.count(repository.EventAccessed) != 1 {
		t.Error("cancelled follower added an event to the leader's successful generation")
	}
	result := collectThumbnailAccessResponse(t, f.start(http.MethodGet, path, "after-cancel", context.Background(), nil))
	assertThumbnailAccessSourceResponse(t, f.thumbnailAccessSourceFixture, result, http.MethodGet)
	assertThumbnailAccessSourceLifecycle(t, f.thumbnailAccessSourceFixture, f.current, 1, 1)
	assertThumbnailAccessSourceEvent(t, f.sink, "after-cancel", f.current)
	if f.sink.count(repository.EventAccessed) != 2 {
		t.Error("independent resident hit changed event counts")
	}
	assertThumbDeltas(t, before, "", 1)
}

func TestThumbnailCompletedFollowerFailureRetainsSourceError(t *testing.T) {
	for _, timeout := range []time.Duration{0, 2 * time.Second} {
		runThumbnailCompletedFollowerSourceFailure(t, timeout)
	}
}

func runThumbnailCompletedFollowerSourceFailure(t *testing.T, timeout time.Duration) {
	t.Helper()
	f := newThumbnailFollowerCancelFixture(t, timeout)
	f.store.closeErr = errors.New("provider=https://private.example.test token=private-close-detail")
	path := "/v1/files/" + f.current.Key + "?representation=thumbnail&w=16&h=16"
	leader := f.start(http.MethodGet, path, "thumbnail-leader", context.Background(), nil)
	waitThumbnailAccessSignal(t, f.entered)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	follower := f.start(http.MethodGet, path, "cancel-follower", ctx, nil)
	f.waitJoined(t)
	if timeout == 0 {
		cancel()
		assertThumbnailFollowerClientCancelled(t, follower)
	}
	waitThumbnailAccessSignal(t, f.pause.Context.Done())
	f.unblock()
	leaderResult := collectThumbnailAccessResponse(t, leader)
	assertThumbnailCompletedFollowerSourceFailure(t, leaderResult.response.StatusCode, leaderResult.body)
	before := snapshotThumbCounters(t)
	f.pause.unblock()
	finished := f.finish(t)
	assertThumbnailCompletedFollowerSourceFailure(t, finished.status, finished.body)
	if timeout > 0 {
		result := collectThumbnailAccessResponse(t, follower)
		assertThumbnailCompletedFollowerSourceFailure(t, result.response.StatusCode, result.body)
	}
	assertThumbnailFollowerCancelLifecycle(t, f)
	assertThumbDeltas(t, before, "source_error", 0)
}

func assertThumbnailCompletedFollowerSourceFailure(t *testing.T, status int, body []byte) {
	t.Helper()
	if status != http.StatusInternalServerError || !bytes.Contains(body, []byte(`"code":"InternalError"`)) || !bytes.Contains(body, []byte(`"message":"internal server error"`)) {
		t.Errorf("source failure overwritten: status=%d body=%q", status, body)
	}
	for _, private := range []string{"private.example.test", "private-close-detail", "provider=", "token="} {
		if bytes.Contains(body, []byte(private)) {
			t.Errorf("source failure leaked %q", private)
		}
	}
}
