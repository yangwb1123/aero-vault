package rest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type contentMetadataOnlyCase struct {
	method string
	header string
	value  string
	status int
}

func TestContentStreamFailureHeadAndConditionsDoNotRead(t *testing.T) {
	fixture := newContentFailureFixture(t, "with-data")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		cases := []contentMetadataOnlyCase{
			{http.MethodHead, "", "", http.StatusOK},
			{http.MethodHead, "Range", "bytes=0-3", http.StatusOK},
			{http.MethodGet, "If-None-Match", `"` + selected.ETag + `"`, http.StatusNotModified},
			{http.MethodGet, "If-Match", `"wrong"`, http.StatusPreconditionFailed},
			{http.MethodGet, "Range", "bytes=99999-", http.StatusRequestedRangeNotSatisfiable},
		}
		for _, endpoint := range contentFailurePaths(fixture, selected) {
			for _, check := range cases {
				assertContentFailureMetadataOnly(t, fixture, selected, endpoint, check)
			}
		}
	}
}

func assertContentFailureMetadataOnly(t *testing.T, fixture *contentFailureFixture, selected repository.Object, endpoint string, check contentMetadataOnlyCase) {
	t.Helper()
	opens := fixture.store.opens.Load()
	response, body, err := contentFailureRequestMethod(t, check.method, endpoint, map[string]string{check.header: check.value})
	if err != nil || response.StatusCode != check.status {
		t.Fatalf("metadata-only path became stream failure: response=%v body=%q err=%v", response, body, err)
	}
	if fixture.store.reads.Load() != 0 || fixture.store.opens.Load() != fixture.store.closes.Load() {
		t.Fatal("metadata-only path read or leaked content")
	}
	wantOpens := opens + contentMetadataOnlyOpens(fixture, selected, check)
	if fixture.store.opens.Load() != wantOpens {
		t.Errorf("metadata-only path changed opener boundary: got=%d want=%d", fixture.store.opens.Load(), wantOpens)
	}
	if check.status < 400 && len(body) != 0 {
		t.Errorf("metadata-only response has body: %q", body)
	}
}

func contentMetadataOnlyOpens(fixture *contentFailureFixture, selected repository.Object, check contentMetadataOnlyCase) int64 {
	if check.method != http.MethodGet {
		return 0
	}
	if selected.VersionID == fixture.older.VersionID || check.status == http.StatusRequestedRangeNotSatisfiable {
		return 1
	}
	return 0
}

func TestContentStreamFailurePrefixRemainsRedactedErrorBeforePartialHeaders(t *testing.T) {
	fixture := newContentFailureFixture(t, "with-data")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentFailurePaths(fixture, selected) {
			opens := fixture.store.opens.Load()
			response, body, err := contentFailureRequest(t, endpoint, map[string]string{"Range": "bytes=4-7"})
			if err != nil || response.StatusCode != http.StatusInternalServerError {
				t.Fatalf("prefix failure became partial response: response=%v body=%q err=%v", response, body, err)
			}
			assertContentFailurePrefixError(t, response, body)
			if fixture.store.opens.Load() != opens+1 || fixture.store.closes.Load() != opens+1 || fixture.store.key.Load() != selected.StorageKey {
				t.Fatal("prefix failure reopened, leaked or switched source")
			}
		}
	}
}

func assertContentFailurePrefixError(t *testing.T, response *http.Response, body []byte) {
	t.Helper()
	assertNoContentHeaderMetadata(t, response)
	if strings.Contains(string(body), "private") || strings.Contains(string(body), "credentials") || response.Header.Get("Content-Range") != "" {
		t.Errorf("prefix failure leaked content or source details: %q", body)
	}
}

func TestContentStreamFailurePrefixPreservesClassWithoutDiagnostics(t *testing.T) {
	for _, check := range []struct {
		err    error
		status int
	}{
		{service.ErrObjectCorrupt, http.StatusGone},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{service.ErrForbidden, http.StatusForbidden},
		{io.ErrUnexpectedEOF, http.StatusInternalServerError},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		(&Handler{}).writeContentSourceError(recorder, request, fmt.Errorf("private backend: %w", check.err))
		if recorder.Code != check.status || strings.Contains(recorder.Body.String(), "private") {
			t.Errorf("prefix error class/details changed: code=%d want=%d body=%q", recorder.Code, check.status, recorder.Body.String())
		}
	}
}

func TestContentStreamFailureExplicitNewRequestCanRecover(t *testing.T) {
	fixture := newContentFailureFixture(t, "tail")
	endpoint := contentFailurePaths(fixture, fixture.current)[1]
	response, body, err := contentFailureRequest(t, endpoint, nil)
	assertContentFailureInterrupted(t, fixture, fixture.current, response, body, err, 1)
	fixture.store.mode.Store("healthy")
	assertHealthyContentFailureRead(t, fixture, fixture.current, endpoint, false)
}

type failingContentWriter struct {
	header http.Header
	writes int
}

func (w *failingContentWriter) Header() http.Header { return w.header }
func (w *failingContentWriter) WriteHeader(int)     {}
func (w *failingContentWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("private writer connection failure")
}

func TestContentStreamFailureWriterErrorAbortsWithoutErrorBodyOrDetails(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "range"}[partial], func(t *testing.T) {
			var logs bytes.Buffer
			handler := &Handler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
			writer := &failingContentWriter{header: http.Header{}}
			request := httptest.NewRequest(http.MethodGet, "/v1/files/key", nil)
			if partial {
				request.Header.Set("Range", "bytes=0-1")
			}
			defer assertContentWriterAbort(t, writer, &logs)
			reader := &objectContentReader{ctx: request.Context(), source: strings.NewReader("body")}
			handler.serveOpenedContent(writer, request, reader, repository.Object{Size: 4}, false)
		})
	}
}

func assertContentWriterAbort(t *testing.T, writer *failingContentWriter, logs *bytes.Buffer) {
	t.Helper()
	if recovered := recover(); recovered != http.ErrAbortHandler {
		t.Errorf("writer failure swallowed or changed: panic=%v", recovered)
	}
	if writer.writes != 1 || !strings.Contains(logs.String(), "content response interrupted") || strings.Contains(logs.String(), "private") {
		t.Errorf("writer failure retried/appended error or exposed details: writes=%d logs=%q", writer.writes, logs.String())
	}
}

func TestContentStreamFailureShortFullCopyAborts(t *testing.T) {
	handler := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Errorf("short full copy finished normally: panic=%v", recovered)
		}
	}()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	reader := &objectContentReader{ctx: request.Context(), source: strings.NewReader("short")}
	handler.serveOpenedContent(httptest.NewRecorder(), request, reader, repository.Object{Size: 99}, false)
}
