package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func TestWriteThumbnailSourceErrorRedactsAndLogsFingerprint(t *testing.T) {
	const detail = "provider=https://objects.example.test request-token=redact-123"
	const requestID = "thumbnail-redaction-request"
	var logs bytes.Buffer
	h := &Handler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	r := httptest.NewRequest(http.MethodGet, "/v1/files/photo.png/thumbnail", nil)
	r.Header.Set("X-Request-ID", requestID)
	rec := httptest.NewRecorder()
	mw.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.writeThumbnailSourceError(w, r, errors.New(detail))
	})).ServeHTTP(rec, r)

	payload := decodeThumbnailError(t, rec)
	if rec.Code != http.StatusInternalServerError || payload.Code != "InternalError" {
		t.Fatalf("response=(%d,%q) want (500,InternalError)", rec.Code, payload.Code)
	}
	if payload.Message != thumbnailInternalErrorMessage {
		t.Fatalf("message=%q want %q", payload.Message, thumbnailInternalErrorMessage)
	}
	logText := logs.String()
	if strings.Contains(logText, detail) || strings.Contains(logText, "request-token") {
		t.Fatalf("raw provider detail or token appeared in log: %q", logText)
	}
	if !strings.Contains(logText, "request_id="+requestID) {
		t.Fatalf("request ID missing from log: %q", logText)
	}
	if !strings.Contains(logText, "error_fingerprint="+thumbnailSourceFingerprint(errors.New(detail))) {
		t.Fatalf("error fingerprint missing from log: %q", logText)
	}
}

func TestWriteThumbnailSourceErrorPreservesKnownErrors(t *testing.T) {
	const detail = "provider=https://objects.example.test/bucket token=known-456"
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{"not found", fmt.Errorf("%s: %w", detail, service.ErrNotFound), http.StatusNotFound, "NotFound", "object not found"},
		{"repository not found", fmt.Errorf("%s: %w", detail, repository.ErrNotFound), http.StatusNotFound, "NotFound", "object not found"},
		{"timeout", fmt.Errorf("%s: %w", detail, context.DeadlineExceeded), http.StatusGatewayTimeout, "Timeout", "request timed out"},
		{"service timeout", fmt.Errorf("%s: %w", detail, service.ErrTimeout), http.StatusGatewayTimeout, "Timeout", "request timed out"},
		{"corrupt", fmt.Errorf("%s: %w", detail, service.ErrObjectCorrupt), http.StatusGone, "ObjectCorrupt", "object is marked as corrupt"},
		{"decode", errors.Join(thumbnail.ErrUnsupported, errors.New(detail)), http.StatusBadRequest, "InvalidArgument", "invalid arguments: thumbnail: unsupported or invalid image"},
		{"format", fmt.Errorf("%s: %w", detail, thumbnail.ErrUnsupportedFormat), http.StatusUnsupportedMediaType, "UnsupportedMediaType", `thumbnail: unsupported image format "webp" (supported: image/jpeg, image/png, image/gif)`},
		{"wrapped content encoding", &thumbnail.OpenError{Err: fmt.Errorf("%s: %w", detail, thumbnail.ErrUnsupportedContentEncoding)}, http.StatusUnsupportedMediaType, "UnsupportedMediaType", thumbnail.ErrUnsupportedContentEncoding.Error()},
		{"image size", fmt.Errorf("%s: %w", detail, thumbnail.ErrImageTooLarge), http.StatusRequestEntityTooLarge, "ImageTooLarge", thumbnail.ErrImageTooLarge.Error()},
		{"metadata size", fmt.Errorf("%s: %w", detail, thumbnail.ErrMetadataTooLarge), http.StatusRequestEntityTooLarge, "MetadataTooLarge", thumbnail.ErrMetadataTooLarge.Error()},
		{"source size", fmt.Errorf("%s: %w", detail, thumbnail.ErrSourceTooLarge), http.StatusRequestEntityTooLarge, "SourceTooLarge", thumbnail.ErrSourceTooLarge.Error()},
		{"nil source", fmt.Errorf("%s: %w", detail, thumbnail.ErrNilSource), http.StatusInternalServerError, "InternalError", "thumbnail source unavailable"},
		{"exact cancellation", context.Canceled, http.StatusInternalServerError, "InternalError", "context canceled"},
		{"wrapped cancellation", fmt.Errorf("%s: %w", detail, context.Canceled), http.StatusInternalServerError, "InternalError", thumbnailInternalErrorMessage},
		{"invalid args", fmt.Errorf("%s: %w", detail, service.ErrInvalidArgs), http.StatusBadRequest, "InvalidArgument", "invalid arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			r := httptest.NewRequest(http.MethodGet, "/v1/files/photo.png/thumbnail", nil)
			rec := httptest.NewRecorder()
			h.writeThumbnailSourceError(rec, r, tc.err)
			payload := decodeThumbnailError(t, rec)
			if rec.Code != tc.wantStatus || payload.Code != tc.wantCode || payload.Message != tc.wantMsg {
				t.Fatalf("response=(%d,%q,%q), want (%d,%q,%q)", rec.Code, payload.Code, payload.Message, tc.wantStatus, tc.wantCode, tc.wantMsg)
			}
			if strings.Contains(rec.Body.String(), detail) {
				t.Fatalf("provider detail leaked into response: %q", rec.Body.String())
			}
		})
	}
}

func TestWriteThumbnailGenerateErrorPreservesServiceTimeout(t *testing.T) {
	const detail = "provider timeout endpoint=https://objects.example.test token=timeout-456"
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/v1/files/photo.png/thumbnail", nil)
	rec := httptest.NewRecorder()
	h.writeThumbnailGenerateError(rec, r, fmt.Errorf("%s: %w", detail, service.ErrTimeout))
	payload := decodeThumbnailError(t, rec)
	if rec.Code != http.StatusGatewayTimeout || payload.Code != "Timeout" || payload.Message != "request timed out" {
		t.Fatalf("response=(%d,%q,%q), want (504,Timeout,%q)", rec.Code, payload.Code, payload.Message, "request timed out")
	}
	if strings.Contains(rec.Body.String(), detail) || strings.Contains(rec.Body.String(), "pipeline") {
		t.Fatalf("timeout detail leaked into response: %q", rec.Body.String())
	}
}

func decodeThumbnailError(t *testing.T, rec *httptest.ResponseRecorder) errorPayload {
	t.Helper()
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v (body=%q)", err, rec.Body.String())
	}
	return body.Error
}

type prefixErrorStorage struct {
	storage.Storage
	err    error
	gets   atomic.Int64
	closes atomic.Int64
}

func (s *prefixErrorStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets.Add(1)
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	return &prefixErrorReadCloser{ReadCloser: rc, err: s.err, closes: &s.closes}, info, nil
}

type prefixErrorReadCloser struct {
	io.ReadCloser
	err    error
	closes *atomic.Int64
}

func (r *prefixErrorReadCloser) Read([]byte) (int, error) { return 0, r.err }

func (r *prefixErrorReadCloser) Close() error {
	r.closes.Add(1)
	return r.ReadCloser.Close()
}

func newPrefixErrorThumbnailREST(t *testing.T, sourceErr error) (*httptest.Server, *Handler, *prefixErrorStorage, *bytes.Buffer, repository.Repository) {
	t.Helper()
	ctx := context.Background()
	dir := restFixtureSchemaDir(t, ctx, "prefix-error.db")
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "prefix-error.db"))
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
	store := &prefixErrorStorage{Storage: realStore, err: sourceErr}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := NewHandler(service.NewFileService(store, repo, nil), logger).
		WithThumbnailCache(thumbnail.NewCache(1<<20, 0)).
		WithThumbnailDecodeAdmission(thumbnail.NewDecodeAdmission(1))
	r := chi.NewRouter()
	r.Use(mw.RequestID)
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = repo.Close()
	})
	return srv, h, store, &logs, repo
}

func TestThumbnailGenericMIMEPrefixErrorIsRedactedAtREST(t *testing.T) {
	const detail = "provider=https://objects.example.test/bucket/prefix request-token=prefix-789"
	for _, tc := range []struct {
		name        string
		contentType string
	}{
		{name: "octet-stream", contentType: "application/octet-stream"},
		{name: "absent content type", contentType: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h, store, logs, repo := newPrefixErrorThumbnailREST(t, errors.New(detail))
			if err := repo.SetBucketVersioning(context.Background(), "default", "default", true); err != nil {
				t.Fatalf("enable versioning: %v", err)
			}
			u := srv.URL + "/v1/files/prefix.png"
			headers := map[string]string{}
			if tc.contentType != "" {
				headers["Content-Type"] = tc.contentType
			}
			if resp, _ := req(t, http.MethodPut, u, pngBytes(t, 8, 8), headers); resp.StatusCode != http.StatusCreated {
				t.Fatalf("PUT status = %d, want %d", resp.StatusCode, http.StatusCreated)
			}
			pre := snapshotThumbCounters(t)
			requestID := "generic-redaction-request"
			resp, body := req(t, http.MethodGet, u+"/thumbnail", nil, map[string]string{"X-Request-ID": requestID})
			payload := decodeThumbnailErrorBytes(t, body)
			if resp.StatusCode != http.StatusInternalServerError || payload.Code != "InternalError" || payload.Message != thumbnailInternalErrorMessage {
				t.Fatalf("response=(%d,%q,%q), want (500,InternalError,%q); body=%q", resp.StatusCode, payload.Code, payload.Message, thumbnailInternalErrorMessage, body)
			}
			assertRedactedThumbnailFailure(t, resp, body,
				"objects.example.test", "bucket", "prefix", "prefix-789")
			assertThumbDeltas(t, pre, "source_error", 0)
			if got := store.gets.Load(); got != 1 {
				t.Fatalf("storage Get count = %d, want 1", got)
			}
			if got := store.closes.Load(); got != 1 {
				t.Fatalf("source Close count = %d, want exactly once", got)
			}
			if h.thumbnailCache.Len() != 0 || h.thumbnailCache.Bytes() != 0 {
				t.Fatalf("failed thumbnail populated cache: len=%d bytes=%d", h.thumbnailCache.Len(), h.thumbnailCache.Bytes())
			}
			logText := logs.String()
			if strings.Contains(logText, detail) || !strings.Contains(logText, "request_id="+requestID) {
				t.Fatalf("log detail/request correlation invalid: %q", logText)
			}
			if !strings.Contains(logText, "error_fingerprint="+thumbnailSourceFingerprint(errors.New(detail))) {
				t.Fatalf("log fingerprint missing: %q", logText)
			}
		})
	}
}

func decodeThumbnailErrorBytes(t *testing.T, body []byte) errorPayload {
	t.Helper()
	var response errorBody
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode error response: %v (body=%q)", err, body)
	}
	return response.Error
}

func assertRedactedThumbnailFailure(t *testing.T, resp *http.Response, body []byte, details ...string) {
	t.Helper()
	for _, detail := range details {
		if bytes.Contains(body, []byte(detail)) {
			t.Fatalf("provider detail %q leaked into response body %q", detail, body)
		}
	}
	for _, header := range []string{"ETag", "Last-Modified", "Cache-Control", "X-Version-Id"} {
		if got := resp.Header.Get(header); got != "" {
			t.Fatalf("failure response emitted success header %s=%q", header, got)
		}
	}
	if resp.Header.Get("Content-Type") == "image/jpeg" {
		t.Fatal("failure response advertised image/jpeg")
	}
	if img, format, err := image.Decode(bytes.NewReader(body)); err == nil && format == "jpeg" {
		t.Fatalf("failure response contained a generated JPEG: bounds=%s", img.Bounds())
	}
}
