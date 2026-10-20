package rest

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func selectedRangeDecimalBody(fixture *contentHeaderFixture, selected repository.Object) string {
	if selected.VersionID == fixture.older.VersionID {
		return "<h1>old attachment</h1>"
	}
	return "<h1>current attachment</h1>"
}

func TestContentRangeDecimalSignsUseFullSelectedContent(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, value := range []string{"bytes=+1-3", "bytes=1-+3", "bytes=0--1", "bytes=-+3", "bytes=+1-3,0-2"} {
				opens, closes := fixture.store.opens.Load(), fixture.store.closes.Load()
				response, body := contentHeaderRequest(t, http.MethodGet, endpoint, map[string]string{"Range": value})
				if response.StatusCode != http.StatusOK || string(body) != selectedRangeDecimalBody(fixture, selected) || response.Header.Get("Content-Range") != "" {
					t.Errorf("signed syntax sliced content: range=%q code=%d body=%q", value, response.StatusCode, body)
				}
				assertContentHeaderMetadata(t, response, selected, pinned)
				assertContentHeaderStream(t, fixture, selected, opens+1, closes+1)
			}
		}
	}
}

func TestContentRangeDecimalOversizedCoordinates(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		pinned := selected.VersionID == fixture.older.VersionID
		for _, endpoint := range contentHeaderPaths(fixture, selected, pinned) {
			for _, check := range []struct {
				header string
				offset int64
			}{
				{"bytes=1-9223372036854775808", 1},
				{"bytes=1-" + strings.Repeat("9", 128), 1},
				{"bytes=-" + strings.Repeat("9", 128), 0},
			} {
				assertContentRangeDecimalSpan(t, fixture, selected, endpoint, check.header, check.offset, pinned)
			}
		}
	}
}

func assertContentRangeDecimalSpan(t *testing.T, fixture *contentHeaderFixture, selected repository.Object, endpoint, value string, offset int64, pinned bool) {
	t.Helper()
	opens, closes := fixture.store.opens.Load(), fixture.store.closes.Load()
	response, body := contentHeaderRequest(t, http.MethodGet, endpoint, map[string]string{"Range": value})
	want := selectedRangeDecimalBody(fixture, selected)[offset:]
	contentRange := fmt.Sprintf("bytes %d-%d/%d", offset, selected.Size-1, selected.Size)
	if response.StatusCode != http.StatusPartialContent || string(body) != want || response.Header.Get("Content-Range") != contentRange {
		t.Errorf("large valid range lost selected span: range=%q status=%d body=%q headers=%v", value, response.StatusCode, body, response.Header)
	}
	assertContentHeaderMetadata(t, response, selected, pinned)
	assertContentHeaderStream(t, fixture, selected, opens+1, closes+1)
}

func TestContentRangeDecimalOversizedStartIsUnsatisfiable(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentHeaderPaths(fixture, selected, selected.VersionID == fixture.older.VersionID) {
			for _, value := range []string{"bytes=9223372036854775808-", "bytes=" + strings.Repeat("9", 128) + "-"} {
				opens, reads := fixture.store.opens.Load(), fixture.store.reads.Load()
				response, _ := contentHeaderRequest(t, http.MethodGet, endpoint, map[string]string{"Range": value})
				if response.StatusCode != http.StatusRequestedRangeNotSatisfiable || response.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", selected.Size) {
					t.Errorf("oversized start fell back to content: range=%q status=%d", value, response.StatusCode)
				}
				assertNoContentHeaderMetadata(t, response)
				assertContentHeaderStream(t, fixture, selected, opens+1, opens+1)
				if fixture.store.reads.Load() != reads {
					t.Error("unsatisfiable range read content")
				}
			}
		}
	}
}

func TestContentRangeDecimalHeadAndConditionsKeepPrecedence(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentHeaderPaths(fixture, selected, selected.VersionID == fixture.older.VersionID) {
			for _, check := range []struct {
				method string
				header string
				value  string
				status int
			}{
				{http.MethodHead, "If-Match", `"` + selected.ETag + `"`, http.StatusOK},
				{http.MethodGet, "If-None-Match", `"` + selected.ETag + `"`, http.StatusNotModified},
				{http.MethodGet, "If-Match", `"wrong"`, http.StatusPreconditionFailed},
			} {
				headers := map[string]string{"Range": "bytes=9223372036854775808-", check.header: check.value}
				response, _ := contentHeaderRequest(t, check.method, endpoint, headers)
				if response.StatusCode != check.status || fixture.store.reads.Load() != 0 {
					t.Errorf("range overrode metadata-only condition: method=%s code=%d reads=%d", check.method, response.StatusCode, fixture.store.reads.Load())
				}
			}
		}
	}
}

func TestContentRangeTruncatedSpanAborts(t *testing.T) {
	handler := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Range", "bytes=0-3")
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Errorf("short Range body completed normally: panic=%v", recovered)
		}
	}()
	reader := &objectContentReader{ctx: request.Context(), source: strings.NewReader("ab")}
	handler.serveOpenedContent(httptest.NewRecorder(), request, reader, repository.Object{Size: 10}, false)
}

func assertContentRangeEmptyPlainRead(t *testing.T, fixture *contentHeaderFixture, selected repository.Object, endpoint string) {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		opens := fixture.store.opens.Load()
		response, body := contentHeaderRequest(t, method, endpoint, nil)
		if response.StatusCode != http.StatusOK || len(body) != 0 || response.ContentLength != 0 {
			t.Errorf("empty content not readable: method=%s status=%d body=%q", method, response.StatusCode, body)
		}
		assertContentHeaderMetadata(t, response, selected, selected.VersionID == fixture.older.VersionID)
		if method == http.MethodGet {
			opens++
		}
		assertContentHeaderStream(t, fixture, selected, opens, opens)
	}
}

func TestContentRangeDecimalEmptyObject(t *testing.T) {
	fixture := newContentHeaderFixture(t, "literal/thumbnail")
	fixture.older = putContentHeaderVersion(t, fixture.svc, fixture.current.Key, "empty-old", nil, "identity")
	fixture.current = putContentHeaderVersion(t, fixture.svc, fixture.current.Key, "empty-current", nil, "identity")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentHeaderPaths(fixture, selected, selected.VersionID == fixture.older.VersionID) {
			assertContentRangeEmptyPlainRead(t, fixture, selected, endpoint)
			for _, value := range []string{"bytes=0-0", "bytes=0-9223372036854775808", "bytes=-9223372036854775808"} {
				response, _ := contentHeaderRequest(t, http.MethodGet, endpoint, map[string]string{"Range": value})
				if response.StatusCode != http.StatusRequestedRangeNotSatisfiable || response.Header.Get("Content-Range") != "bytes */0" {
					t.Errorf("valid range on empty content returned success: range=%q status=%d", value, response.StatusCode)
				}
				assertNoContentHeaderMetadata(t, response)
			}
		}
	}
}
