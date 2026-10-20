package rest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func repeatedReadRequest(t *testing.T, method, endpoint string, headers http.Header) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header = headers.Clone()
	request.Header.Set("Authorization", "Bearer reader")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func assertRepeatedRead(t *testing.T, response *http.Response, body []byte, selected repository.Object, method string, status int, pinned bool) {
	t.Helper()
	if response.StatusCode != status {
		t.Fatalf("repeated read headers picked a field instead of the condition: method=%s status=%d want=%d body=%q", method, response.StatusCode, status, body)
	}
	if status == http.StatusOK || status == http.StatusNotModified {
		assertContentHeaderMetadata(t, response, selected, pinned)
	}
	if method == http.MethodHead || status == http.StatusNotModified {
		if len(body) != 0 {
			t.Errorf("bodyless read returned %q", body)
		}
	} else if status == http.StatusOK && int64(len(body)) != selected.Size {
		t.Errorf("full selected object changed: bytes=%d want=%d", len(body), selected.Size)
	}
}

func TestRepeatedReadHeadersIfMatchCombinesFields(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				headers := http.Header{"If-Match": {`"wrong"`, `"` + selected.ETag + `"`},
					"If-Unmodified-Since": {selected.UpdatedAt.Add(-time.Hour).UTC().Format(http.TimeFormat)}}
				response, body := repeatedReadRequest(t, method, endpoint, headers)
				assertRepeatedRead(t, response, body, selected, method, http.StatusOK, pinned)
			}
		}
	}
}

func TestRepeatedReadHeadersIfNoneMatchCombinesFields(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				headers := http.Header{"If-None-Match": {`"wrong"`, `W/"` + selected.ETag + `"`},
					"If-Modified-Since": {selected.UpdatedAt.Add(-time.Hour).UTC().Format(http.TimeFormat)}}
				response, body := repeatedReadRequest(t, method, endpoint, headers)
				assertRepeatedRead(t, response, body, selected, method, http.StatusNotModified, pinned)
			}
		}
	}
}

func TestRepeatedReadHeadersIfRangeRequiresSingleField(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, first := range []string{`"` + selected.ETag + `"`, selected.UpdatedAt.UTC().Format(http.TimeFormat)} {
				for _, span := range []string{"bytes=0-3", "bytes=99999-"} {
					opens, closes := fixture.store.opens.Load(), fixture.store.closes.Load()
					headers := http.Header{"Range": {span}, "If-Range": {first, first}}
					response, body := repeatedReadRequest(t, http.MethodGet, endpoint, headers)
					assertRepeatedRead(t, response, body, selected, http.MethodGet, http.StatusOK, pinned)
					assertContentHeaderStream(t, fixture, selected, opens+1, closes+1)
				}
			}
		}
	}
}

func TestRepeatedReadHeadersDatesAreNotLists(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		past := selected.UpdatedAt.Add(-time.Hour).UTC().Format(http.TimeFormat)
		future := selected.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, headers := range []http.Header{{"If-Modified-Since": {future, past}}, {"If-Unmodified-Since": {past, future}}} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					response, body := repeatedReadRequest(t, method, endpoint, headers)
					assertRepeatedRead(t, response, body, selected, method, http.StatusOK, pinned)
				}
			}
		}
	}
}

func TestRepeatedReadHeadersRangeRequiresSingleField(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, ranges := range [][]string{{"bytes=0-3", "bytes=4-7"}, {"bytes=99999-", "bytes=0-3"}} {
				opens, closes := fixture.store.opens.Load(), fixture.store.closes.Load()
				response, body := repeatedReadRequest(t, http.MethodGet, endpoint, http.Header{"Range": ranges})
				assertRepeatedRead(t, response, body, selected, http.MethodGet, http.StatusOK, pinned)
				assertContentHeaderStream(t, fixture, selected, opens+1, closes+1)
			}
		}
	}
}

func TestRepeatedReadHeadersSingleRangeFieldKeepsFirstSegment(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			opens, closes := fixture.store.opens.Load(), fixture.store.closes.Load()
			response, body := repeatedReadRequest(t, http.MethodGet, endpoint, http.Header{"Range": {"bytes=0-3,8-11"}})
			if response.StatusCode != http.StatusPartialContent || len(body) != 4 {
				t.Fatalf("single-field range compatibility changed: status=%d body=%q", response.StatusCode, body)
			}
			assertContentHeaderMetadata(t, response, selected, pinned)
			assertContentHeaderStream(t, fixture, selected, opens+1, closes+1)
		}
	}
}

func TestRepeatedReadHeadersMalformedListsDoNotPickFirstValue(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for name, status := range map[string]int{"If-Match": http.StatusPreconditionFailed, "If-None-Match": http.StatusOK} {
				for _, fields := range [][]string{{"*", `"wrong"`}, {`"` + selected.ETag + `"`, `"unterminated`}} {
					headers := http.Header{name: fields, "If-Modified-Since": {selected.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)}}
					response, body := repeatedReadRequest(t, http.MethodGet, endpoint, headers)
					assertRepeatedRead(t, response, body, selected, http.MethodGet, status, pinned)
				}
			}
		}
	}
}

func TestRepeatedReadHeadersEmptyFieldLinesDoNotHideList(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for name, status := range map[string]int{"If-Match": http.StatusOK, "If-None-Match": http.StatusNotModified} {
				headers := http.Header{name: {"", `"` + selected.ETag + `"`, " \t"}}
				dateHeader := "If-Unmodified-Since"
				if name == "If-None-Match" {
					dateHeader = "If-Modified-Since"
				}
				headers[dateHeader] = []string{selected.UpdatedAt.Add(-time.Hour).UTC().Format(http.TimeFormat)}
				response, body := repeatedReadRequest(t, http.MethodGet, endpoint, headers)
				assertRepeatedRead(t, response, body, selected, http.MethodGet, status, pinned)
			}
		}
	}
}

func TestRepeatedReadHeadersEmptyIfNoneMatchSuppressesDate(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			headers := http.Header{"If-None-Match": {""}, "If-Modified-Since": {selected.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)}}
			response, body := repeatedReadRequest(t, http.MethodGet, endpoint, headers)
			assertRepeatedRead(t, response, body, selected, http.MethodGet, http.StatusOK, pinned)
		}
	}
}

func TestRepeatedReadHeadersEmptyIfRangeDoesNotAuthorizePartial(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			response, body := repeatedReadRequest(t, http.MethodGet, endpoint, http.Header{"Range": {"bytes=0-3"}, "If-Range": {""}})
			assertRepeatedRead(t, response, body, selected, http.MethodGet, http.StatusOK, pinned)
		}
	}
}

func TestRepeatedReadHeadersUnmodifiedPrecedesIfNoneMatch(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			headers := http.Header{"If-None-Match": {`"wrong"`, `"` + selected.ETag + `"`},
				"If-Unmodified-Since": {selected.UpdatedAt.Add(-time.Hour).UTC().Format(http.TimeFormat)}}
			response, body := repeatedReadRequest(t, http.MethodGet, endpoint, headers)
			assertRepeatedRead(t, response, body, selected, http.MethodGet, http.StatusPreconditionFailed, pinned)
		}
	}
}

func TestReadConditionHeadersKeepOpaqueCommasAndOriginalFields(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/files/key", nil)
	request.Header = http.Header{"If-Match": {`"other,tag"`, `"opaque,tag"`}}
	original := request.Header.Clone()
	object := repository.Object{ETag: "opaque,tag"}
	if readPreconditionFailed(request, object) {
		t.Error("opaque tag comma was split or later field ignored")
	}
	if !reflect.DeepEqual(request.Header, original) {
		t.Errorf("read condition changed request headers: got=%v want=%v", request.Header, original)
	}
}

func TestRepeatedReadHeadersDenyPrecedesConditions(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				fixture.svc.WithAuthorizer(&readBoundaryProvider{rejectAt: 1})
				headers := http.Header{"If-None-Match": {`"wrong"`, `"` + selected.ETag + `"`}, "Range": {"bytes=0-3", "bytes=4-7"}}
				response, body := repeatedReadRequest(t, method, endpoint, headers)
				if response.StatusCode != http.StatusForbidden || fixture.store.opens.Load() != 0 {
					t.Fatalf("repeated conditions bypassed Deny: status=%d opens=%d body=%q", response.StatusCode, fixture.store.opens.Load(), body)
				}
				assertNoContentHeaderMetadata(t, response)
			}
		}
	}
}
