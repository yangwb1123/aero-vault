package rest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// The existing seam exposes real version A at the first Stat and real B at
// the conditional re-Stat. Unlike opener/coalesced-result checks, this guard
// must also reject when neither the cache nor the opener will be consulted.
func thumbnailRevalidationPreconditionFailures(f *thumbnailRevalidationCacheFixture) []http.Header {
	a, b := thumbnailEncodingValidator(f.older), thumbnailEncodingValidator(f.current)
	return []http.Header{
		{"If-Match": {a}, "If-None-Match": {a, b}},
		{"If-Match": {a}, "If-Modified-Since": {f.current.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)}},
		{"If-Match": {a}, "If-None-Match": {a}},
		{"If-Match": {a, "W/" + b}, "If-None-Match": {a, b}},
	}
}

func TestThumbnailRevalidationPreconditionChangedStrongTagIsTerminal(t *testing.T) {
	for _, hot := range []bool{false, true} {
		f := newThumbnailRevalidationCacheFixture(t)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for path, endpoint := range f.paths() {
				for condition, headers := range thumbnailRevalidationPreconditionFailures(f) {
					name := fmt.Sprintf("hot=%t/%s/path=%d/condition=%d", hot, method, path, condition)
					t.Run(name, func(t *testing.T) {
						if !hot {
							f.cache.InvalidateSource(thumbnailSourceIdentity(f.current))
						}
						checkThumbnailRevalidationPrecondition(t, f, method, endpoint, headers, http.StatusPreconditionFailed, f.current, 2)
					})
				}
			}
		}
	}
}

func checkThumbnailRevalidationPrecondition(t *testing.T, f *thumbnailRevalidationCacheFixture, method, endpoint string, headers http.Header, status int, selected repository.Object, stats int64) {
	t.Helper()
	hits, misses, _, _ := f.cache.Stats()
	entries, size := f.cache.Len(), f.cache.Bytes()
	before, before304 := snapshotThumbCounters(t), thumbnail304Counter(t)
	beforeAccess := f.sink.count(repository.EventAccessed)
	provider := &readBoundaryProvider{}
	f.svc.WithAuthorizer(provider)
	response, body := thumbnailEmptyTagRequest(t, f.thumbnailEncodingOpenFixture, method, endpoint, headers)
	if response.StatusCode != status {
		t.Errorf("fresh strong precondition lost: method=%s headers=%v status=%d want=%d", method, headers, response.StatusCode, status)
	}
	if status == http.StatusPreconditionFailed {
		assertThumbnailMediaOpenErrorBody(t, body, method, "PreconditionFailed")
		assertThumbnailMediaOpenErrorHeaders(t, response)
	} else {
		assertThumbnailRevalidationPrecondition304(t, response, body, selected)
		before304++
	}
	assertThumbDeltas(t, before, "", 0)
	if thumbnail304Counter(t) != before304 || f.sink.count(repository.EventAccessed) != beforeAccess {
		t.Error("metadata-only precondition changed 304/access outcomes")
	}
	if provider.reads.Load() != 2 || f.repo.calls.Load() != stats {
		t.Errorf("conditional observations were skipped/retried: authorizations=%d stats=%d want stats=%d", provider.reads.Load(), f.repo.calls.Load(), stats)
	}
	assertThumbnailRevalidationPreconditionIdle(t, f, hits, misses, entries, size)
}

func assertThumbnailRevalidationPreconditionIdle(t *testing.T, f *thumbnailRevalidationCacheFixture, hits, misses uint64, entries int, size int64) {
	t.Helper()
	afterHits, afterMisses, _, _ := f.cache.Stats()
	if afterHits != hits || afterMisses != misses || f.cache.Len() != entries || f.cache.Bytes() != size {
		t.Errorf("metadata-only precondition touched cache: hits=%d misses=%d entries=%d bytes=%d", afterHits, afterMisses, f.cache.Len(), f.cache.Bytes())
	}
	if f.store.opens.Load() != 0 || f.store.reads.Load() != 0 || f.store.closes.Load() != 0 {
		t.Errorf("metadata-only precondition touched source: opens=%d reads=%d closes=%d", f.store.opens.Load(), f.store.reads.Load(), f.store.closes.Load())
	}
}

func assertThumbnailRevalidationPrecondition304(t *testing.T, response *http.Response, body []byte, selected repository.Object) {
	t.Helper()
	if len(body) != 0 {
		t.Errorf("304 returned %d body bytes", len(body))
	}
	for name, want := range map[string]string{
		"ETag": thumbnailEncodingValidator(selected), "X-Version-Id": selected.VersionID,
		"Last-Modified": selected.UpdatedAt.UTC().Format(http.TimeFormat),
		"Cache-Control": fmt.Sprintf("private, max-age=%d, must-revalidate", thumbFreshnessMaxAge),
	} {
		if response.Header.Get(name) != want {
			t.Errorf("304 lost selected %s: got=%q want=%q", name, response.Header.Get(name), want)
		}
	}
}

func TestThumbnailRevalidationPreconditionMatchingStrongTagsKeep304(t *testing.T) {
	f := newThumbnailRevalidationCacheFixture(t)
	a, b := thumbnailEncodingValidator(f.older), thumbnailEncodingValidator(f.current)
	conditions := []http.Header{
		{"If-Match": {a, b}, "If-None-Match": {a, b}, "If-Unmodified-Since": {"Wed, 21 Oct 2015 07:28:00 GMT"}},
		{"If-Match": {"*"}, "If-None-Match": {"*"}},
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			for _, headers := range conditions {
				checkThumbnailRevalidationPrecondition(t, f, method, endpoint, headers, http.StatusNotModified, f.current, 2)
			}
		}
	}
}

func TestThumbnailRevalidationPreconditionHistoricalPinUsesOwnTag(t *testing.T) {
	f := newThumbnailRevalidationCacheFixture(t)
	a := thumbnailEncodingValidator(f.older)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			headers := http.Header{"If-Match": {a}, "If-None-Match": {a}}
			checkThumbnailRevalidationPrecondition(t, f, method, endpoint+"&version="+f.older.VersionID, headers, http.StatusNotModified, f.older, 0)
		}
	}
}
