package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func thumbnailEmptyTagMatchingConditions(selected repository.Object, bound bool) [][]string {
	conditions := [][]string{{"*"}, {" \t*\t "}, {"", "*"}}
	if bound {
		etag := thumbnailEncodingValidator(selected)
		conditions = append(conditions, []string{`""`, etag}, []string{`W/"", "other", W/` + etag}, []string{`"", ` + etag})
	}
	return conditions
}

func TestThumbnailEmptyTagWildcardAndMatchingListsKeepMetadataOnly304(t *testing.T) {
	for _, bound := range []bool{true, false} {
		f := newThumbnailEmptyTagFixture(t, bound)
		for _, selected := range []repository.Object{f.current, f.older} {
			for _, endpoint := range thumbnailEmptyTagPaths(f, selected) {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, values := range thumbnailEmptyTagMatchingConditions(selected, bound) {
						checkThumbnailEmptyTag304(t, f, selected, method, endpoint, values)
					}
				}
			}
		}
	}
}

func checkThumbnailEmptyTag304(t *testing.T, f *thumbnailEncodingOpenFixture, selected repository.Object, method, endpoint string, values []string) {
	t.Helper()
	before, before304 := snapshotThumbCounters(t), thumbnail304Counter(t)
	response, body := thumbnailEmptyTagRequest(t, f, method, endpoint, http.Header{"If-None-Match": values})
	if response.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("valid match lost metadata-only 304: method=%s condition=%q status=%d body=%q", method, values, response.StatusCode, body)
	}
	etag, version, cacheControl := "", "", "no-store"
	if f.store.bound {
		etag, version = thumbnailEncodingValidator(selected), selected.VersionID
		cacheControl = fmt.Sprintf("private, max-age=%d, must-revalidate", thumbFreshnessMaxAge)
	}
	for name, want := range map[string]string{"ETag": etag, "X-Version-Id": version, "Cache-Control": cacheControl,
		"Last-Modified": selected.UpdatedAt.UTC().Format(http.TimeFormat)} {
		if response.Header.Get(name) != want {
			t.Errorf("304 lost selected %s: got=%q want=%q", name, response.Header.Get(name), want)
		}
	}
	wantStats := int64(0)
	if selected.VersionID == f.current.VersionID {
		wantStats = 2
	}
	if f.repo.calls.Load() != wantStats || thumbnail304Counter(t) != before304+1 {
		t.Error("304 skipped conditional re-observation or changed its counter")
	}
	assertThumbnailMediaRevalidationNoSource(t, f)
	assertThumbDeltas(t, before, "", 0)
}

func TestThumbnailEmptyTagQuotedStarAndInvalidListsAreNotWildcards(t *testing.T) {
	f := newThumbnailEmptyTagFixture(t, true)
	for _, selected := range []repository.Object{f.current, f.older} {
		for _, endpoint := range thumbnailEmptyTagPaths(f, selected) {
			baseline, jpeg := thumbnailEmptyTagRequest(t, f, http.MethodGet, endpoint, http.Header{})
			assertThumbnailEncodingOpenHealthy(t, baseline, jpeg, http.MethodGet)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, values := range [][]string{{`"*"`}, {`W/"*"`}, {`*, ""`}, {`"", *`}, {`""junk`}, {"*", `""`}} {
					checkThumbnailEmptyTagNonmatch(t, f, selected, method, endpoint, values, jpeg, false)
				}
			}
		}
	}
}

func TestThumbnailEmptyTagStrongPreconditionRemainsPrior(t *testing.T) {
	f := newThumbnailEmptyTagFixture(t, true)
	for _, selected := range []repository.Object{f.current, f.older} {
		for _, endpoint := range thumbnailEmptyTagPaths(f, selected) {
			baseline, jpeg := thumbnailEmptyTagRequest(t, f, http.MethodGet, endpoint, http.Header{})
			assertThumbnailEncodingOpenHealthy(t, baseline, jpeg, http.MethodGet)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, value := range []string{`""`, `W/""`} {
					beforeAccess, before304 := f.sink.count(repository.EventAccessed), thumbnail304Counter(t)
					hits, misses, _, _ := f.cache.Stats()
					headers := http.Header{"If-Match": {value}, "If-None-Match": {`""`}}
					response, _ := thumbnailEmptyTagRequest(t, f, method, endpoint, headers)
					assertThumbnailEmptyTagDenied(t, f, response, http.StatusPreconditionFailed, beforeAccess, before304, hits, misses)
				}
			}
		}
	}
}

func assertThumbnailEmptyTagDenied(t *testing.T, f *thumbnailEncodingOpenFixture, response *http.Response, status int, access int, before304 float64, hits, misses uint64) {
	t.Helper()
	if response.StatusCode != status || response.Header.Get("ETag") != "" || response.Header.Get("X-Version-Id") != "" {
		t.Errorf("conditional bypassed rejection/leaked identity: status=%d want=%d headers=%v", response.StatusCode, status, response.Header)
	}
	assertThumbnailEmptyTagRejectedSourceIdle(t, f)
	afterHits, afterMisses, _, _ := f.cache.Stats()
	if afterHits != hits || afterMisses != misses || f.sink.count(repository.EventAccessed) != access || thumbnail304Counter(t) != before304 {
		t.Error("rejected conditional entered cache/access/304 path")
	}
}

func assertThumbnailEmptyTagRejectedSourceIdle(t *testing.T, f *thumbnailEncodingOpenFixture) {
	t.Helper()
	if f.store.opens.Load() != 0 || f.store.reads.Load() != 0 || f.store.closes.Load() != 0 {
		t.Error("rejected conditional touched source")
	}
}

func TestThumbnailEmptyTagAuthorizationFailureRemainsTerminal(t *testing.T) {
	f := newThumbnailEmptyTagFixture(t, true)
	baseline, jpeg := thumbnailEmptyTagRequest(t, f, http.MethodGet, f.paths()[0], http.Header{})
	assertThumbnailEncodingOpenHealthy(t, baseline, jpeg, http.MethodGet)
	for _, failure := range []error{nil, errors.New("private empty-tag authorization failure")} {
		for _, endpoint := range f.paths() {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				provider := &readBoundaryProvider{rejectAt: 1, failure: failure}
				f.svc.WithAuthorizer(provider)
				beforeAccess, before304 := f.sink.count(repository.EventAccessed), thumbnail304Counter(t)
				hits, misses, _, _ := f.cache.Stats()
				response, _ := thumbnailEmptyTagRequest(t, f, method, endpoint, http.Header{"If-None-Match": {`""`}})
				status := http.StatusForbidden
				if failure != nil {
					status = http.StatusInternalServerError
				}
				assertThumbnailEmptyTagDenied(t, f, response, status, beforeAccess, before304, hits, misses)
				if provider.reads.Load() != 1 {
					t.Error("authorization failure was retried")
				}
			}
		}
	}
}

func TestThumbnailEmptyTagRawExactContentKeepsConditionSemantics(t *testing.T) {
	f := newContentFailureFixture(t, "healthy")
	for _, selected := range []repository.Object{f.current, f.older} {
		for _, endpoint := range contentFailurePaths(f, selected) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				opens := f.store.opens.Load()
				headers := map[string]string{"If-None-Match": `""`,
					"If-Modified-Since": selected.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)}
				response, body, err := contentFailureRequestMethod(t, method, endpoint, headers)
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("raw exact content changed: response=%v body=%q err=%v", response, body, err)
				}
				assertContentHeaderMetadataIdentity(t, response, selected, f)
				wantBytes := selected.Size
				if method == http.MethodHead {
					wantBytes = 0
				} else {
					opens++
				}
				if int64(len(body)) != wantBytes || f.store.opens.Load() != opens || f.store.closes.Load() != opens {
					t.Error("raw content body/HEAD/close behavior changed")
				}
			}
		}
	}
}

func TestThumbnailEmptyTagOpenAPIContract(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct{ Description string }
		}
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct{ path, method string }{
		{"/v1/files/{key}", "get"}, {"/v1/files/{key}", "head"}, {"/v1/files/{key}/thumbnail", "get"},
	} {
		description := spec.Paths[operation.path][operation.method].Responses["304"].Description
		if !strings.Contains(description, "empty opaque tags are not wildcards") {
			t.Errorf("%s %s missing empty-tag contract: %q", operation.method, operation.path, description)
		}
	}
}
