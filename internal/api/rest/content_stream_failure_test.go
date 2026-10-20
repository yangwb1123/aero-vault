package rest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type contentFailureStore struct {
	storage.Storage
	mode   atomic.Value
	key    atomic.Value
	opens  atomic.Int64
	closes atomic.Int64
	reads  atomic.Int64
}

func (s *contentFailureStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	s.key.Store(key)
	s.opens.Add(1)
	return &contentFailureSource{ReadCloser: rc, store: s}, info, nil
}

type contentFailureSource struct {
	io.ReadCloser
	store *contentFailureStore
}

func (r *contentFailureSource) Read(p []byte) (int, error) {
	r.store.reads.Add(1)
	n, err := r.ReadCloser.Read(p)
	mode := r.store.mode.Load().(string)
	if n > 0 && mode == "corrupt" {
		p[0] ^= 1
	}
	if n > 0 && mode == "data-eof" {
		return n, io.EOF
	}
	if n > 0 && mode == "with-data" {
		return n, errors.New("private backend error s3://credentials@private-host/object")
	}
	if err == io.EOF && mode == "tail" {
		return n, errors.New("private backend tail verification failed")
	}
	return n, err
}

func (r *contentFailureSource) Close() error {
	r.store.closes.Add(1)
	if err := r.ReadCloser.Close(); err != nil {
		return err
	}
	if r.store.mode.Load().(string) == "close" {
		return errors.New("private backend close failure s3://credentials@private-host/object")
	}
	return nil
}

type contentFailureFixture struct {
	store   *contentFailureStore
	web     *httptest.Server
	older   repository.Object
	current repository.Object
}

func newContentFailureFixture(t *testing.T, mode string) *contentFailureFixture {
	t.Helper()
	_, repo, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := &contentFailureStore{Storage: local}
	store.mode.Store(mode)
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{}).
		WithReadVerification(service.ReadVerificationConfig{Enabled: mode == "corrupt"})
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	older := putContentSelectionFixture(t, svc, "literal/thumbnail", []byte("old selected bytes"), "text/plain")
	current := putContentSelectionFixture(t, svc, older.Key, []byte("current selected bytes"), "text/plain")
	return &contentFailureFixture{store: store, web: serveLegacyDenialFixture(t, svc, repo), older: older, current: current}
}

// A new non-reusable transport makes this one explicit request, not an automatic
// idempotent retry after the server aborts a previously reused connection.
func contentFailureRequest(t *testing.T, endpoint string, headers map[string]string) (*http.Response, []byte, error) {
	t.Helper()
	return contentFailureRequestMethod(t, http.MethodGet, endpoint, headers)
}

func contentFailureRequestMethod(t *testing.T, method, endpoint string, headers map[string]string) (*http.Response, []byte, error) {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer reader")
	for name, value := range headers {
		if name != "" {
			request.Header.Set(name, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return response, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return response, body, err
}

func assertContentFailureInterrupted(t *testing.T, fixture *contentFailureFixture, selected repository.Object, response *http.Response, body []byte, err error, opens int64) {
	t.Helper()
	if err == nil {
		t.Errorf("failed stream finished normally: status=%d body=%q", response.StatusCode, body)
	}
	if fixture.store.opens.Load() != opens || fixture.store.closes.Load() != opens || fixture.store.key.Load() != selected.StorageKey {
		t.Errorf("failed stream reopened/leaked: opens=%d closes=%d key=%v want=%s", fixture.store.opens.Load(), fixture.store.closes.Load(), fixture.store.key.Load(), selected.StorageKey)
	}
	if strings.Contains(string(body), "internal server error") || strings.Contains(string(body), "private backend") {
		t.Errorf("error text appended/exposed on content stream: %q", body)
	}
}

func contentFailurePaths(fixture *contentFailureFixture, selected repository.Object) []string {
	version := ""
	if selected.VersionID == fixture.older.VersionID {
		version = selected.VersionID
	}
	return readBoundaryContentPaths(fixture.web.URL+"/v1", selected.Key, version)
}

func TestContentStreamFailureHealthyAndEOFWithDataRemainReadable(t *testing.T) {
	for _, mode := range []string{"healthy", "data-eof"} {
		fixture := newContentFailureFixture(t, mode)
		for _, selected := range []repository.Object{fixture.current, fixture.older} {
			for _, endpoint := range contentFailurePaths(fixture, selected) {
				for _, partial := range []bool{false, true} {
					assertHealthyContentFailureRead(t, fixture, selected, endpoint, partial)
				}
			}
		}
	}
}

func assertHealthyContentFailureRead(t *testing.T, fixture *contentFailureFixture, selected repository.Object, endpoint string, partial bool) {
	t.Helper()
	want := "current selected bytes"
	if selected.VersionID == fixture.older.VersionID {
		want = "old selected bytes"
	}
	headers, status := map[string]string{}, http.StatusOK
	if partial {
		headers["Range"], status, want = "bytes=0-3", http.StatusPartialContent, want[:4]
	}
	opens := fixture.store.opens.Load()
	response, body, err := contentFailureRequest(t, endpoint, headers)
	if err != nil || response.StatusCode != status || string(body) != want {
		t.Fatalf("healthy content changed: response=%v body=%q err=%v", response, body, err)
	}
	assertContentHeaderMetadataIdentity(t, response, selected, fixture)
	if fixture.store.opens.Load() != opens+1 || fixture.store.closes.Load() != opens+1 || fixture.store.key.Load() != selected.StorageKey {
		t.Fatal("healthy read reopened, leaked or selected a different source")
	}
}

func assertContentHeaderMetadataIdentity(t *testing.T, response *http.Response, selected repository.Object, fixture *contentFailureFixture) {
	t.Helper()
	if response.Header.Get("ETag") != `"`+selected.ETag+`"` {
		t.Errorf("read lost selected validator: %v", response.Header)
	}
	wantVersion := ""
	if selected.VersionID == fixture.older.VersionID {
		wantVersion = selected.VersionID
	}
	if response.Header.Get("X-Version-Id") != wantVersion {
		t.Errorf("read lost selected pin: got=%q want=%q", response.Header.Get("X-Version-Id"), wantVersion)
	}
}

func TestContentStreamFailureReportedTailDoesNotFinishNormally(t *testing.T) {
	for _, mode := range []string{"tail", "with-data", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newContentFailureFixture(t, mode)
			for _, selected := range []repository.Object{fixture.current, fixture.older} {
				for _, endpoint := range contentFailurePaths(fixture, selected) {
					opens := fixture.store.opens.Load()
					response, body, err := contentFailureRequest(t, endpoint, nil)
					assertContentFailureInterrupted(t, fixture, selected, response, body, err, opens+1)
				}
			}
		})
	}
}

func TestContentStreamFailureRangeWithDataErrorDoesNotFinishNormally(t *testing.T) {
	fixture := newContentFailureFixture(t, "with-data")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentFailurePaths(fixture, selected) {
			opens := fixture.store.opens.Load()
			response, body, err := contentFailureRequest(t, endpoint, map[string]string{"Range": "bytes=0-3"})
			assertContentFailureInterrupted(t, fixture, selected, response, body, err, opens+1)
		}
	}
}
