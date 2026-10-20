package rest

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func TestThumbnailAccessSourceMetadataPreservesOnlyOpenedScalars(t *testing.T) {
	obj := repository.Object{ID: 72, TenantID: "selected-tenant", Bucket: "selected-bucket", Key: "exact/key",
		VersionID: "selected-version", Backend: "local", Size: 99, ETag: "selected-etag", ContentType: "image/png",
		UpdatedAt: time.Unix(200, 0).UTC(), StorageKey: "not-an-event-field", Metadata: map[string]string{"secret": "not-retained"}}
	stat := repository.Object{ID: 1, TenantID: "old-tenant", Bucket: "old-bucket", Key: "old-key", Backend: "old-backend",
		VersionID: "old-version", ETag: "old-etag", Size: 1, ContentType: "old-type"}
	want := obj
	want.StorageKey, want.Metadata = "", nil
	for _, bound := range []bool{true, false} {
		for _, blocked := range []bool{true, false} {
			opened := thumbnailOpenedSource(service.ThumbnailSource{Object: obj, Bound: bound}, blocked)
			if opened.Bound != bound || opened.CacheStoreBlocked != blocked || !reflect.DeepEqual(thumbnailAccessObject(stat, &opened), want) {
				t.Errorf("opened metadata/proof/policy mixed with preflight: opened=%+v accessed=%+v", opened, thumbnailAccessObject(stat, &opened))
			}
		}
	}
	if !reflect.DeepEqual(thumbnailAccessObject(stat, nil), stat) {
		t.Error("resident hit did not retain the authorized Stat")
	}
	empty := thumbnailOpenedSource(service.ThumbnailSource{}, false)
	if !reflect.DeepEqual(thumbnailAccessObject(stat, &empty), repository.Object{}) {
		t.Error("incomplete opened metadata inherited preflight fields")
	}
}

func TestThumbnailAccessSourceResidentMissAndHistoryKeepOwnEvents(t *testing.T) {
	f := newThumbnailAccessSourceFixture(t)
	f.repo.older = f.current
	f.unblock()
	path := "/v1/files/" + f.current.Key + "?representation=thumbnail&w=16&h=16"
	for _, id := range []string{"thumbnail-cold", "thumbnail-hot"} {
		result := collectThumbnailAccessResponse(t, f.start(http.MethodGet, path, id, context.Background(), nil))
		assertThumbnailAccessSourceResponse(t, f, result, http.MethodGet)
		assertThumbnailAccessSourceEvent(t, f.sink, id, f.current)
		assertThumbnailAccessSourceLifecycle(t, f, f.current, 1, 1)
	}
	pin := path + "&version=" + f.older.VersionID
	result := collectThumbnailAccessResponse(t, f.start(http.MethodGet, pin, "thumbnail-pin", context.Background(), nil))
	if result.response.StatusCode != http.StatusOK || result.response.Header.Get("ETag") != thumbnailEncodingValidator(f.older) || result.response.Header.Get("X-Version-Id") != f.older.VersionID {
		t.Errorf("pin lost its source: status=%d headers=%v", result.response.StatusCode, result.response.Header)
	}
	assertThumbnailAccessSourceEvent(t, f.sink, "thumbnail-pin", f.older)
	assertThumbnailAccessSourceLifecycle(t, f, f.older, 2, 2)
	assertThumbnailAccessSource304(t, f, path)
}

func assertThumbnailAccessSourceLifecycle(t *testing.T, f *thumbnailAccessSourceFixture, selected repository.Object, opens int64, entries int) {
	t.Helper()
	if f.store.opens.Load() != opens || f.store.closes.Load() != opens || f.store.key.Load() != selected.StorageKey || f.cache.Len() != entries {
		t.Errorf("resident/miss/pin changed lifecycle: opens=%d closes=%d key=%v entries=%d", f.store.opens.Load(), f.store.closes.Load(), f.store.key.Load(), f.cache.Len())
	}
}

func assertThumbnailAccessSource304(t *testing.T, f *thumbnailAccessSourceFixture, path string) {
	t.Helper()
	headers := map[string]string{"If-None-Match": thumbnailEncodingValidator(f.current)}
	result := collectThumbnailAccessResponse(t, f.start(http.MethodGet, path, "thumbnail-304", context.Background(), headers))
	if result.response.StatusCode != http.StatusNotModified || f.sink.count(repository.EventAccessed) != 3 || f.store.opens.Load() != 2 {
		t.Errorf("304 changed access/open semantics: status=%d access=%d opens=%d", result.response.StatusCode, f.sink.count(repository.EventAccessed), f.store.opens.Load())
	}
}

func TestThumbnailAccessSourceRejectedFollowerDoesNotEmit(t *testing.T) {
	f := newThumbnailAccessSourceFixture(t)
	path := "/v1/files/" + f.current.Key + "?representation=thumbnail&w=16&h=16"
	leader := f.start(http.MethodGet, path, "thumbnail-leader", context.Background(), nil)
	waitThumbnailAccessSignal(t, f.entered)
	headers := map[string]string{"If-Match": thumbnailEncodingValidator(f.older)}
	follower := f.start(http.MethodGet, path, "thumbnail-follower", context.Background(), headers)
	waitThumbnailAccessSignal(t, f.waiting)
	f.unblock()
	assertThumbnailAccessSourceResponse(t, f, collectThumbnailAccessResponse(t, leader), http.MethodGet)
	result := collectThumbnailAccessResponse(t, follower)
	if result.response.StatusCode != http.StatusPreconditionFailed || f.sink.count(repository.EventAccessed) != 1 || f.store.opens.Load() != 1 || f.store.closes.Load() != 1 {
		t.Errorf("rejected follower emitted/accessed/opened: status=%d access=%d opens=%d closes=%d", result.response.StatusCode, f.sink.count(repository.EventAccessed), f.store.opens.Load(), f.store.closes.Load())
	}
	assertThumbnailAccessSourceEvent(t, f.sink, "thumbnail-leader", f.current)
}

// First-Stat denial precedes thumbnailDerive's Vary declaration. Do not use
// the media-rejection helper, whose assertions require a successful Stat.
func assertThumbnailAccessSourceDeniedHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	for _, name := range []string{"ETag", "Last-Modified", "X-Version-Id", "Cache-Control", "Content-Encoding", "Content-Range"} {
		if response.Header.Get(name) != "" {
			t.Errorf("first-Stat denial certified %s=%q", name, response.Header.Get(name))
		}
	}
	for _, name := range []string{"Authorization", "X-Aero-Tenant", "X-Api-Key"} {
		if strings.Contains(response.Header.Get("Vary"), name) {
			t.Errorf("first-Stat denial unexpectedly reached derivation's Vary declaration: %s", name)
		}
	}
}

type thumbnailAccessSourceProvider struct {
	failure error
	reads   atomic.Int64
}

func (p *thumbnailAccessSourceProvider) Authorize(ctx context.Context, _ access.Principal, action access.Action, _ access.Resource) (access.Decision, error) {
	if action == access.ActionRead && mw.RequestIDFrom(ctx) == "thumbnail-follower" {
		p.reads.Add(1)
		return access.Decision{Reason: "explicit_deny"}, p.failure
	}
	return access.Decision{Allowed: true}, nil
}

func TestThumbnailAccessSourceDenyAndProviderFailureCannotJoin(t *testing.T) {
	for _, failure := range []error{nil, errors.New("access source provider unavailable")} {
		f := newThumbnailAccessSourceFixture(t)
		provider := &thumbnailAccessSourceProvider{failure: failure}
		f.svc.WithAuthorizer(provider)
		path := "/v1/files/" + f.current.Key + "?representation=thumbnail&w=16&h=16"
		leader := f.start(http.MethodGet, path, "thumbnail-leader", context.Background(), nil)
		waitThumbnailAccessSignal(t, f.entered)
		result := collectThumbnailAccessResponse(t, f.start(http.MethodGet, path, "thumbnail-follower", context.Background(), nil))
		want := http.StatusForbidden
		if failure != nil {
			want = http.StatusInternalServerError
		}
		_, misses, _, _ := f.cache.Stats()
		if result.response.StatusCode != want || provider.reads.Load() != 1 || misses != 1 || f.sink.count(repository.EventAccessed) != 1 || f.store.opens.Load() != 1 {
			t.Errorf("unauthorized follower joined flight: status=%d checks=%d misses=%d access=%d opens=%d", result.response.StatusCode, provider.reads.Load(), misses, f.sink.count(repository.EventAccessed), f.store.opens.Load())
		}
		assertThumbnailAccessSourceDeniedHeaders(t, result.response)
		f.unblock()
		assertThumbnailAccessSourceResponse(t, f, collectThumbnailAccessResponse(t, leader), http.MethodGet)
		assertThumbnailAccessSourceEvent(t, f.sink, "thumbnail-leader", f.current)
	}
}
