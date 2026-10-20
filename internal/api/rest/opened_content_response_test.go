package rest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type openedContentRepo struct {
	repository.Repository
	stale repository.Object
	armed atomic.Bool
	calls atomic.Int64
}

func (r *openedContentRepo) GetObject(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	if r.armed.Load() && key == r.stale.Key && r.calls.Add(1) == 1 {
		return r.stale, nil
	}
	return r.Repository.GetObject(ctx, tenant, bucket, key)
}

type openedContentStore struct {
	storage.Storage
	key    atomic.Value
	opens  atomic.Int64
	reads  atomic.Int64
	closes atomic.Int64
	onOpen func()
	onRead func()
}

func (s *openedContentStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	s.key.Store(key)
	s.opens.Add(1)
	if s.onOpen != nil {
		s.onOpen()
	}
	return &openedContentSource{ReadCloser: rc, store: s}, info, nil
}

type openedContentSource struct {
	io.ReadCloser
	store *openedContentStore
}

func (r *openedContentSource) Read(p []byte) (int, error) {
	r.store.reads.Add(1)
	n, err := r.ReadCloser.Read(p)
	if r.store.onRead != nil {
		r.store.onRead()
	}
	return n, err
}

func (r *openedContentSource) Close() error {
	r.store.closes.Add(1)
	return r.ReadCloser.Close()
}

type openedContentFixture struct {
	handler http.Handler
	repo    *openedContentRepo
	store   *openedContentStore
	stale   repository.Object
	current repository.Object
	data    string
}

func newOpenedContentFixture(t *testing.T, staleData, currentData string) *openedContentFixture {
	t.Helper()
	_, base, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	repo := &openedContentRepo{Repository: base}
	store := &openedContentStore{Storage: local}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	stale := putContentSelectionFixture(t, svc, "literal/metadata", []byte(staleData), "application/octet-stream")
	current := putContentSelectionFixture(t, svc, stale.Key, []byte(currentData), "text/plain")
	stale.UpdatedAt = current.UpdatedAt.Add(-time.Hour)
	repo.stale = stale
	handler := NewRouter(svc, repo, nil, nil, nil, nil, nil, slog.Default(), false, nil, nil, 0, false)
	return &openedContentFixture{handler: handler, repo: repo, store: store, stale: stale, current: current, data: currentData}
}

func (f *openedContentFixture) request(headers map[string]string) *httptest.ResponseRecorder {
	return f.requestWithContext(context.Background(), headers)
}

func (f *openedContentFixture) requestWithContext(ctx context.Context, headers map[string]string) *httptest.ResponseRecorder {
	ctx = access.WithPrincipal(ctx, access.Principal{
		SubjectID: "reader", TenantID: service.DefaultTenant, Kind: access.PrincipalUser, Scopes: []string{"read"},
	})
	request := httptest.NewRequest(http.MethodGet, contentSelectionURL("", f.current.Key, "content", ""), nil).WithContext(ctx)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func assertOpenedContentSource(t *testing.T, fixture *openedContentFixture) {
	t.Helper()
	if fixture.store.opens.Load() != 1 || fixture.store.closes.Load() != 1 || fixture.store.key.Load() != fixture.current.StorageKey {
		t.Errorf("selected stream reopened/leaked: opens=%d closes=%d key=%v want=%s", fixture.store.opens.Load(), fixture.store.closes.Load(), fixture.store.key.Load(), fixture.current.StorageKey)
	}
}

func assertOpenedContentHeaders(t *testing.T, response *httptest.ResponseRecorder, object repository.Object) {
	t.Helper()
	if response.Header().Get("ETag") != `"`+object.ETag+`"` || response.Header().Get("Content-Type") != object.ContentType ||
		response.Header().Get("Last-Modified") != object.UpdatedAt.UTC().Format(http.TimeFormat) || response.Header().Get("X-Version-Id") != "" {
		t.Errorf("response describes preflight, not selected content: headers=%v selected=%+v", response.Header(), object)
	}
}

func TestOpenedContentPreflightCanFinishWithoutBlob(t *testing.T) {
	for _, matching := range []bool{false, true} {
		fixture := newOpenedContentFixture(t, "old stale content!!", "NEW CONTENT")
		fixture.repo.armed.Store(true)
		headers := map[string]string{"If-Match": `"wrong"`, "Range": "bytes=9999-"}
		want := http.StatusPreconditionFailed
		if matching {
			headers = map[string]string{"If-None-Match": `"` + fixture.stale.ETag + `"`, "Range": "bytes=9999-"}
			want = http.StatusNotModified
		}
		response := fixture.request(headers)
		if response.Code != want || fixture.store.opens.Load() != 0 || fixture.repo.calls.Load() != 1 {
			t.Errorf("metadata-only decision opened another source: status=%d opens=%d lookups=%d", response.Code, fixture.store.opens.Load(), fixture.repo.calls.Load())
		}
	}
}

func TestOpenedContentRangeHeadersDescribeSelectedStream(t *testing.T) {
	fixture := newOpenedContentFixture(t, "old stale content!!", "NEW CONTENT")
	fixture.repo.armed.Store(true)
	response := fixture.request(map[string]string{"Range": "bytes=2-5"})
	assertOpenedContentSource(t, fixture)
	assertOpenedContentHeaders(t, response, fixture.current)
	if response.Code != http.StatusPartialContent || response.Body.String() != fixture.data[2:6] ||
		response.Header().Get("Content-Range") != fmt.Sprintf("bytes 2-5/%d", fixture.current.Size) || response.Header().Get("Content-Length") != "4" {
		t.Errorf("range used preflight metadata: status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestOpenedContentPreconditionsRecheckSelectedObject(t *testing.T) {
	for _, name := range []string{"If-Match", "If-None-Match", "If-Unmodified-Since"} {
		t.Run(name, func(t *testing.T) {
			fixture := newOpenedContentFixture(t, "old stale content!!", "NEW CONTENT")
			fixture.repo.armed.Store(true)
			value, want := `"`+fixture.stale.ETag+`"`, http.StatusPreconditionFailed
			if name == "If-None-Match" {
				value, want = `"`+fixture.current.ETag+`"`, http.StatusNotModified
			}
			if name == "If-Unmodified-Since" {
				value = fixture.stale.UpdatedAt.Add(time.Minute).UTC().Format(http.TimeFormat)
			}
			response := fixture.request(map[string]string{name: value})
			assertOpenedContentSource(t, fixture)
			if response.Code != want || fixture.store.reads.Load() != 0 {
				t.Errorf("preflight condition authorized another body: header=%s status=%d want=%d reads=%d body=%q", name, response.Code, want, fixture.store.reads.Load(), response.Body.String())
			}
		})
	}
}

func TestOpenedContentConditionsPrecedeRangeForSelectedObject(t *testing.T) {
	for _, cached := range []bool{false, true} {
		fixture := newOpenedContentFixture(t, "old stale content!!", "NEW CONTENT")
		fixture.repo.armed.Store(true)
		headers := map[string]string{"If-Match": `"` + fixture.stale.ETag + `"`, "Range": "bytes=99999-"}
		want := http.StatusPreconditionFailed
		if cached {
			headers = map[string]string{"If-None-Match": `"` + fixture.current.ETag + `"`, "Range": "bytes=99999-"}
			want = http.StatusNotModified
		}
		response := fixture.request(headers)
		assertOpenedContentSource(t, fixture)
		if response.Code != want || fixture.store.reads.Load() != 0 {
			t.Errorf("range preceded selected-source conditions: status=%d want=%d reads=%d body=%q", response.Code, want, fixture.store.reads.Load(), response.Body.String())
		}
	}
}

func TestOpenedContentRangeUsesSelectedSize(t *testing.T) {
	for _, test := range []struct {
		stale, current, span, body, contentRange string
		status                                   int
	}{
		{"old", "NEW LONGER CONTENT", "bytes=4-8", "LONGE", "bytes 4-8/18", http.StatusPartialContent},
		{"old longer content", "tiny", "bytes=5-9", "", "bytes */4", http.StatusRequestedRangeNotSatisfiable},
	} {
		t.Run(test.span+test.current, func(t *testing.T) {
			fixture := newOpenedContentFixture(t, test.stale, test.current)
			fixture.repo.armed.Store(true)
			response := fixture.request(map[string]string{"Range": test.span})
			assertOpenedContentSource(t, fixture)
			if response.Code != test.status || response.Header().Get("Content-Range") != test.contentRange ||
				test.body != "" && response.Body.String() != test.body {
				t.Errorf("range used stale size: status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
			}
		})
	}
}

func TestOpenedContentIfRangeChecksSelectedValidator(t *testing.T) {
	for _, test := range []string{"stale", "weak", "wildcard", "invalid", "date-old", "etag", "date"} {
		t.Run(test, func(t *testing.T) {
			fixture := newOpenedContentFixture(t, "old stale content!!", "NEW CONTENT")
			values := map[string]string{
				"stale": `"` + fixture.stale.ETag + `"`, "weak": `W/"` + fixture.current.ETag + `"`,
				"wildcard": "*", "invalid": "invalid", "date-old": fixture.stale.UpdatedAt.UTC().Format(http.TimeFormat),
				"etag": `"` + fixture.current.ETag + `"`, "date": fixture.current.UpdatedAt.UTC().Format(http.TimeFormat),
			}
			response := fixture.request(map[string]string{"Range": "bytes=0-2", "If-Range": values[test]})
			want, body := http.StatusOK, fixture.data
			if test == "etag" || test == "date" {
				want, body = http.StatusPartialContent, fixture.data[:3]
			}
			assertOpenedContentSource(t, fixture)
			assertOpenedContentHeaders(t, response, fixture.current)
			if response.Code != want || response.Body.String() != body || response.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
				t.Errorf("If-Range changed selected response: token=%s status=%d body=%q headers=%v", test, response.Code, response.Body.String(), response.Header())
			}
		})
	}
}

func TestOpenedContentCancellationDropsCurrentBytes(t *testing.T) {
	for _, span := range []string{"", "bytes=0-2", "bytes=4-6"} {
		t.Run(span, func(t *testing.T) {
			fixture := newOpenedContentFixture(t, "old", "NEW CONTENT")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fixture.store.onRead = cancel
			response := fixture.requestWithContext(ctx, map[string]string{"Range": span})
			assertOpenedContentSource(t, fixture)
			if fixture.store.reads.Load() != 1 {
				t.Errorf("cancelled current source kept reading: reads=%d", fixture.store.reads.Load())
			}
			if span == "bytes=4-6" {
				if response.Code == http.StatusPartialContent {
					t.Error("cancelled prefix skip declared partial content")
				}
			} else if response.Body.Len() != 0 {
				t.Errorf("cancelled current response delivered bytes: %q", response.Body.String())
			}
		})
	}
}

func TestOpenedContentCancelledAfterOpenDoesNotRead(t *testing.T) {
	fixture := newOpenedContentFixture(t, "old", "NEW CONTENT")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.store.onOpen = cancel
	response := fixture.requestWithContext(ctx, nil)
	assertOpenedContentSource(t, fixture)
	if fixture.store.reads.Load() != 0 || response.Body.Len() != 0 {
		t.Errorf("already cancelled source was read/delivered: reads=%d body=%q", fixture.store.reads.Load(), response.Body.String())
	}
}

func TestOpenedContentIfRangeDoesNotUsePreflightETag(t *testing.T) {
	for _, matching := range []bool{false, true} {
		fixture := newOpenedContentFixture(t, "old stale content!!", "NEW CONTENT")
		fixture.repo.armed.Store(true)
		value, want, body := fixture.stale.ETag, http.StatusOK, fixture.data
		if matching {
			value, want, body = fixture.current.ETag, http.StatusPartialContent, fixture.data[:3]
		}
		response := fixture.request(map[string]string{"Range": "bytes=0-2", "If-Range": `"` + value + `"`})
		assertOpenedContentSource(t, fixture)
		assertOpenedContentHeaders(t, response, fixture.current)
		if response.Code != want || response.Body.String() != body {
			t.Errorf("If-Range trusted preflight: status=%d body=%q headers=%v", response.Code, response.Body.String(), response.Header())
		}
	}
}
