package rest

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// Reuse the real SQLite/local/generation fixture, but return the same real
// current row at every observation. No replacement or concurrent mutation is
// needed to distinguish an empty opaque tag from the wildcard grammar.
func newThumbnailEmptyTagFixture(t *testing.T, bound bool) *thumbnailEncodingOpenFixture {
	t.Helper()
	f := newThumbnailEncodingOpenFixture(t, "image/png", "", bound)
	f.repo.stale = f.current
	return f
}

func thumbnailEmptyTagPaths(f *thumbnailEncodingOpenFixture, selected repository.Object) []string {
	paths := f.paths()
	if selected.VersionID == f.older.VersionID {
		for i := range paths {
			paths[i] += "&version=" + selected.VersionID
		}
	}
	return paths
}

func thumbnailEmptyTagRequest(t *testing.T, f *thumbnailEncodingOpenFixture, method, endpoint string, headers http.Header) (*http.Response, []byte) {
	t.Helper()
	f.repo.calls.Store(0)
	f.repo.armed.Store(true)
	f.store.opens.Store(0)
	f.store.reads.Store(0)
	f.store.closes.Store(0)
	f.store.key.Store("")
	return repeatedReadRequest(t, method, endpoint, headers)
}

func thumbnailEmptyTagNonmatches() [][]string {
	return [][]string{
		{`""`}, {`W/""`}, {`"other", ""`}, {`W/"", "other"`}, {`"other"`, `""`},
	}
}

func TestThumbnailEmptyTagNonmatchingUsesSelectedSource(t *testing.T) {
	for _, mode := range []struct{ bound, hot bool }{{true, false}, {true, true}, {false, false}} {
		f := newThumbnailEmptyTagFixture(t, mode.bound)
		for _, selected := range []repository.Object{f.current, f.older} {
			for path, endpoint := range thumbnailEmptyTagPaths(f, selected) {
				baseline, jpeg := thumbnailEmptyTagRequest(t, f, http.MethodGet, endpoint, http.Header{})
				assertThumbnailEncodingOpenHealthy(t, baseline, jpeg, http.MethodGet)
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for condition, values := range thumbnailEmptyTagNonmatches() {
						name := fmt.Sprintf("bound=%t/hot=%t/%s/%d/%s/%d", mode.bound, mode.hot, selected.VersionID, path, method, condition)
						t.Run(name, func(t *testing.T) {
							checkThumbnailEmptyTagNonmatch(t, f, selected, method, endpoint, values, jpeg, mode.hot)
						})
					}
				}
			}
		}
	}
}

func checkThumbnailEmptyTagNonmatch(t *testing.T, f *thumbnailEncodingOpenFixture, selected repository.Object, method, endpoint string, values []string, jpeg []byte, hot bool) {
	t.Helper()
	if !hot {
		f.cache.InvalidateSource(thumbnailSourceIdentity(selected))
	}
	headers := http.Header{"If-None-Match": values,
		"If-Modified-Since": {selected.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)}}
	before, before304 := snapshotThumbCounters(t), thumbnail304Counter(t)
	beforeAccess := f.sink.count(repository.EventAccessed)
	hits, _, _, _ := f.cache.Stats()
	response, body := thumbnailEmptyTagRequest(t, f, method, endpoint, headers)
	if response.StatusCode != http.StatusOK {
		t.Errorf("empty opaque tag treated as wildcard: method=%s condition=%q status=%d want=200 headers=%v", method, values, response.StatusCode, response.Header)
		return
	}
	assertThumbnailEncodingOpenHealthy(t, response, body, method)
	assertThumbnailEmptyTagIdentity(t, f, selected, response, len(jpeg))
	if method == http.MethodGet && !bytes.Equal(body, jpeg) {
		t.Error("nonmatching conditional changed the selected JPEG")
	}
	assertThumbnailEmptyTagPipeline(t, f, selected, hot, hits)
	assertThumbDeltas(t, before, "", 1)
	if thumbnail304Counter(t) != before304 || f.sink.count(repository.EventAccessed) != beforeAccess+1 {
		t.Error("nonmatching conditional changed 304/access counters")
	}
}

func assertThumbnailEmptyTagIdentity(t *testing.T, f *thumbnailEncodingOpenFixture, selected repository.Object, response *http.Response, size int) {
	t.Helper()
	etag, version, cacheControl := "", "", "no-store"
	if f.store.bound {
		etag, version = thumbnailEncodingValidator(selected), selected.VersionID
		cacheControl = fmt.Sprintf("private, max-age=%d, must-revalidate", thumbFreshnessMaxAge)
	}
	for name, want := range map[string]string{
		"ETag": etag, "X-Version-Id": version, "Cache-Control": cacheControl,
		"Last-Modified": selected.UpdatedAt.UTC().Format(http.TimeFormat), "Content-Length": strconv.Itoa(size),
	} {
		if response.Header.Get(name) != want {
			t.Errorf("selected response lost %s: got=%q want=%q", name, response.Header.Get(name), want)
		}
	}
}

func assertThumbnailEmptyTagPipeline(t *testing.T, f *thumbnailEncodingOpenFixture, selected repository.Object, hot bool, hits uint64) {
	t.Helper()
	if hot {
		if f.store.opens.Load() != 0 || f.store.reads.Load() != 0 || f.store.closes.Load() != 0 {
			t.Error("resident response opened/read/closed a source")
		}
		hits++
	} else {
		assertThumbnailEncodingOpenSource(t, f, selected, true)
	}
	afterHits, _, _, _ := f.cache.Stats()
	if afterHits != hits {
		t.Errorf("conditional selected wrong cache path: hits=%d want=%d", afterHits, hits)
	}
	if !f.store.bound && (f.cache.Len() != 0 || f.cache.Bytes() != 0) {
		t.Error("unbound source entered resident cache")
	}
}
