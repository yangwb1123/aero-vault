package rest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func TestThumbnailCloseVerificationFailureIsGoneAndNotCached(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "close.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	realStore, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	store := &countingStore{Storage: realStore}
	svc := service.NewFileService(store, repo, nil).
		WithDeleteFailOpen(true).
		WithReadVerification(service.ReadVerificationConfig{Enabled: true})
	h := NewHandler(svc, nil).WithThumbnailCache(thumbnail.NewCache(1<<20, 0))
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = repo.Close()
	})

	url := srv.URL + "/v1/files/verified.png"
	data := pngBytes(t, 64, 64)
	if resp, _ := req(t, "PUT", url, data, map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if err := repo.SetObjectMetaKey(ctx, "default", "default", "verified.png", "_aero_content_md5", strings.Repeat("0", 32)); err != nil {
		t.Fatalf("tamper verification metadata: %v", err)
	}

	for i := 0; i < 2; i++ {
		resp, body := req(t, "GET", url+"/thumbnail?w=32&h=32", nil, nil)
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("request %d status = %d, want 410 (body=%q)", i+1, resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte(`"code":"ObjectCorrupt"`)) {
			t.Fatalf("request %d body = %s, want ObjectCorrupt", i+1, body)
		}
		if got := resp.Header.Get("ETag"); got != "" {
			t.Fatalf("request %d emitted success ETag %q", i+1, got)
		}
		if got := resp.Header.Get("Cache-Control"); got != "" {
			t.Fatalf("request %d emitted success Cache-Control %q", i+1, got)
		}
		if got := resp.Header.Get("Last-Modified"); got != "" {
			t.Fatalf("request %d emitted success Last-Modified %q", i+1, got)
		}
	}
	if got := store.gets.Load(); got != 2 {
		t.Fatalf("storage Get count = %d, want 2 (failed result must not be cached)", got)
	}
	if h.thumbnailCache.Len() != 0 || h.thumbnailCache.Bytes() != 0 {
		t.Fatalf("failed thumbnail populated cache: len=%d bytes=%d", h.thumbnailCache.Len(), h.thumbnailCache.Bytes())
	}
}

type closeErrorStore struct {
	storage.Storage
	closeErr   error
	truncateAt int
	gets       atomic.Int64
	closes     atomic.Int64
}

func (s *closeErrorStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets.Add(1)
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	var reader io.Reader
	if s.truncateAt > 0 {
		reader = io.LimitReader(rc, int64(s.truncateAt))
	}
	return &closeErrorReadCloser{
		ReadCloser: rc,
		reader:     reader,
		closeErr:   s.closeErr,
		closes:     &s.closes,
	}, info, nil
}

type closeErrorReadCloser struct {
	io.ReadCloser
	reader   io.Reader
	closeErr error
	closes   *atomic.Int64
}

func (r *closeErrorReadCloser) Read(p []byte) (int, error) {
	if r.reader != nil {
		return r.reader.Read(p)
	}
	return r.ReadCloser.Read(p)
}

func (r *closeErrorReadCloser) Close() error {
	r.closes.Add(1)
	underlyingErr := r.ReadCloser.Close()
	if r.closeErr != nil {
		return r.closeErr
	}
	return underlyingErr
}

func newCloseErrorThumbnailREST(t *testing.T, closeErr error, truncateAt int) (*httptest.Server, *closeErrorStore, *thumbnail.Cache) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "close-errors.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	realStore, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	store := &closeErrorStore{
		Storage:    realStore,
		closeErr:   closeErr,
		truncateAt: truncateAt,
	}
	cache := thumbnail.NewCache(1<<20, 0)
	h := NewHandler(service.NewFileService(store, repo, nil).WithDeleteFailOpen(true), nil).
		WithThumbnailCache(cache)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = repo.Close()
	})
	return srv, store, cache
}

func TestThumbnailCloseErrorRESTClassificationAndHeaders(t *testing.T) {
	dataLen := len(pngBytes(t, 64, 64))
	cases := []struct {
		name       string
		closeErr   error
		truncateAt int
		wantStatus int
		wantCode   string
		wantReason string
		silent     bool
	}{
		{
			name:       "generic close",
			closeErr:   errors.New("backend close failed"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "InternalError",
			wantReason: "source_error",
		},
		{
			name:       "deadline close",
			closeErr:   context.DeadlineExceeded,
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   "Timeout",
			wantReason: "timeout",
		},
		{
			name:       "canceled close",
			closeErr:   context.Canceled,
			wantReason: "",
			silent:     true,
		},
		{
			name:       "decode and close",
			closeErr:   errors.New("close after decode failure"),
			truncateAt: dataLen / 2,
			wantStatus: http.StatusBadRequest,
			wantCode:   "InvalidArgument",
			wantReason: "unsupported",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, cache := newCloseErrorThumbnailREST(t, tc.closeErr, tc.truncateAt)
			u := srv.URL + "/v1/files/close.png"
			data := pngBytes(t, 64, 64)
			if resp, _ := req(t, "PUT", u, data, map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
				t.Fatalf("PUT status = %d, want %d", resp.StatusCode, http.StatusCreated)
			}
			for i := 0; i < 2; i++ {
				pre := snapshotThumbCounters(t)
				resp, body := req(t, "GET", u+"/thumbnail?w=32&h=32", nil, nil)
				if tc.silent {
					if len(body) != 0 {
						t.Fatalf("request %d canceled-close body = %q, want empty", i+1, body)
					}
				} else {
					if resp.StatusCode != tc.wantStatus {
						t.Fatalf("request %d status = %d, want %d (body=%q)", i+1, resp.StatusCode, tc.wantStatus, body)
					}
					if !bytes.Contains(body, []byte(`"code":"`+tc.wantCode+`"`)) {
						t.Fatalf("request %d body = %s, want %s", i+1, body, tc.wantCode)
					}
				}
				for _, header := range []string{"ETag", "Cache-Control", "Last-Modified"} {
					if got := resp.Header.Get(header); got != "" {
						t.Fatalf("request %d emitted failure %s %q", i+1, header, got)
					}
				}
				assertThumbDeltas(t, pre, tc.wantReason, 0)
			}
			if got := store.gets.Load(); got != 2 {
				t.Fatalf("storage Get count = %d, want 2 (failed result must not be cached)", got)
			}
			if got := store.closes.Load(); got != 2 {
				t.Fatalf("source Close count = %d, want exactly once per request", got)
			}
			if cache.Len() != 0 || cache.Bytes() != 0 {
				t.Fatalf("failed thumbnail populated cache: len=%d bytes=%d", cache.Len(), cache.Bytes())
			}
		})
	}
}
