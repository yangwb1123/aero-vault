package rest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type versionCancellationStore struct {
	storage.Storage
	onOpen func()
	onRead func()
	opened string
	source *versionCancellationSource
}

func (s *versionCancellationStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	s.opened = key
	s.source = &versionCancellationSource{ReadCloser: rc, cancel: s.onRead}
	if s.onOpen != nil {
		s.onOpen()
	}
	return s.source, info, nil
}

type versionCancellationSource struct {
	io.ReadCloser
	cancel func()
	reads  int
	bytes  int
	closed int
}

func (s *versionCancellationSource) Read(p []byte) (int, error) {
	s.reads++
	n, err := s.ReadCloser.Read(p[:min(len(p), 1024)])
	s.bytes += n
	if s.reads == 1 && s.cancel != nil {
		s.cancel() // completed read ignores the request context on purpose
	}
	return n, err
}

func (s *versionCancellationSource) Close() error {
	s.closed++
	return s.ReadCloser.Close()
}

func newVersionCancellationFixture(t *testing.T) (http.Handler, *versionCancellationStore, repository.Object) {
	t.Helper()
	_, repo, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatal(err)
	}
	store := &versionCancellationStore{Storage: local}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	old := strings.Repeat("old-secret-", 8192)
	object, err := svc.Put(ctx, "", "", "cancel.txt", strings.NewReader(old), int64(len(old)), service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "", "", "cancel.txt", strings.NewReader("new content"), 11, service.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	handler := NewRouter(svc, repo, nil, nil, nil, nil, nil, slog.Default(), false, nil, nil, 0, false)
	return handler, store, object
}

func requestCancelledVersion(handler http.Handler, object repository.Object, header string, ctx context.Context) *httptest.ResponseRecorder {
	ctx = access.WithPrincipal(ctx, access.Principal{SubjectID: "reader", TenantID: service.DefaultTenant,
		Kind: access.PrincipalUser, Scopes: []string{"read"}})
	request := httptest.NewRequest(http.MethodGet, "/files/cancel.txt?version="+url.QueryEscape(object.VersionID), nil).WithContext(ctx)
	if header != "" {
		request.Header.Set("Range", header)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertCancelledVersionSource(t *testing.T, store *versionCancellationStore, object repository.Object, reads int) {
	t.Helper()
	if store.source == nil || store.opened != object.StorageKey {
		t.Fatalf("did not open exact selected version: got=%q want=%q", store.opened, object.StorageKey)
	}
	if store.source.reads != reads || store.source.bytes > 1024 || store.source.closed != 1 {
		t.Fatalf("cancelled read continued or leaked: reads=%d bytes=%d closed=%d", store.source.reads, store.source.bytes, store.source.closed)
	}
}

func TestVersionReadCancellationStopsPrefixSkip(t *testing.T) {
	handler, store, object := newVersionCancellationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.onRead = cancel
	response := requestCancelledVersion(handler, object, "bytes=65536-65543", ctx)
	assertCancelledVersionSource(t, store, object, 1)
	if response.Code == http.StatusPartialContent || strings.Contains(response.Body.String(), "old-secret-") {
		t.Fatalf("cancelled prefix returned historical bytes: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestVersionReadCancellationDropsBodyAfterRangeHeaders(t *testing.T) {
	handler, store, object := newVersionCancellationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.onRead = cancel
	response := requestCancelledVersion(handler, object, "bytes=0-8191", ctx)
	assertCancelledVersionSource(t, store, object, 1)
	if response.Body.Len() != 0 {
		t.Fatalf("cancelled range delivered bytes or appended JSON after headers: %q", response.Body.String())
	}
}

func TestVersionReadCancellationDropsFullBody(t *testing.T) {
	handler, store, object := newVersionCancellationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.onRead = cancel
	response := requestCancelledVersion(handler, object, "", ctx)
	assertCancelledVersionSource(t, store, object, 1)
	if response.Body.Len() != 0 {
		t.Fatalf("cancelled full response delivered bytes: %q", response.Body.String())
	}
}

func TestVersionReadCancellationAfterOpenDoesNotRead(t *testing.T) {
	handler, store, object := newVersionCancellationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.onOpen = cancel
	response := requestCancelledVersion(handler, object, "bytes=65536-65543", ctx)
	assertCancelledVersionSource(t, store, object, 0)
	if response.Code == http.StatusPartialContent {
		t.Fatal("cancelled prefix advertised success")
	}
}
