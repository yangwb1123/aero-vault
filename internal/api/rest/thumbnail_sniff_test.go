package rest

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

type sniffReadCloser struct {
	data     []byte
	chunk    int
	readErr  error
	closeErr error
	off      int
	calls    atomic.Int32
	closes   atomic.Int32
}

func (r *sniffReadCloser) Read(p []byte) (int, error) {
	r.calls.Add(1)
	if r.readErr != nil {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, r.readErr
	}
	if r.off == len(r.data) {
		return 0, io.EOF
	}
	size := len(p)
	if r.chunk > 0 && size > r.chunk {
		size = r.chunk
	}
	n := copy(p[:size], r.data[r.off:])
	r.off += n
	return n, nil
}

func (r *sniffReadCloser) Close() error {
	r.closes.Add(1)
	return r.closeErr
}

func TestSniffThumbnailSourceAcceptedReplayAndClose(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "jpeg", data: append([]byte{0xff, 0xd8}, []byte("jpeg-payload")...)},
		{name: "png", data: append([]byte{0x89, 'P', 'N', 'G'}, []byte("png-payload")...)},
		{name: "gif", data: append([]byte("GIF89a"), []byte("gif-payload")...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := &sniffReadCloser{data: tc.data, chunk: 3}
			stream, err := sniffThumbnailSource(context.Background(), source)
			if err != nil {
				t.Fatalf("sniffThumbnailSource error = %v", err)
			}
			got, err := io.ReadAll(stream)
			if err != nil {
				t.Fatalf("read replayed stream: %v", err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatalf("replayed bytes = %q, want %q", got, tc.data)
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("close stream: %v", err)
			}
			if got := source.closes.Load(); got != 1 {
				t.Fatalf("source closes = %d, want 1", got)
			}
		})
	}
}

func TestSniffThumbnailSourceRejectsAndClosesOnce(t *testing.T) {
	sourceErr := errors.New("prefix read failed")
	cases := []struct {
		name    string
		data    []byte
		readErr error
		wantErr error
	}{
		{name: "webp", data: webpBytes, wantErr: thumbnail.ErrUnsupportedFormat},
		{name: "unknown", data: []byte("not an image"), wantErr: thumbnail.ErrNotAnImage},
		{name: "short", data: []byte{0xff}, wantErr: thumbnail.ErrNotAnImage},
		{name: "fake gif", data: []byte("GIF90a-not-a-real-gif"), wantErr: thumbnail.ErrNotAnImage},
		{name: "source error", data: []byte("abc"), readErr: sourceErr, wantErr: sourceErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := &sniffReadCloser{data: tc.data, readErr: tc.readErr}
			stream, err := sniffThumbnailSource(context.Background(), source)
			if stream != nil {
				t.Fatal("rejected source returned a stream")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if got := source.closes.Load(); got != 1 {
				t.Fatalf("source closes = %d, want 1", got)
			}
		})
	}
}

type nilSniffReadCloser struct{}

func (*nilSniffReadCloser) Read([]byte) (int, error) {
	panic("typed nil source was read")
}

func (*nilSniffReadCloser) Close() error {
	panic("typed nil source was closed")
}

func TestSniffThumbnailSourceRejectsNilReadersWithoutClose(t *testing.T) {
	var typedNil *nilSniffReadCloser
	for _, tc := range []struct {
		name   string
		source io.ReadCloser
	}{
		{name: "nil", source: nil},
		{name: "typed nil", source: typedNil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("sniffThumbnailSource panicked: %v", recovered)
					}
				}()
				_, err = sniffThumbnailSource(context.Background(), tc.source)
			}()
			if !errors.Is(err, thumbnail.ErrNilSource) {
				t.Fatalf("error = %v, want ErrNilSource", err)
			}
		})
	}
}

func TestClassifyNilThumbnailSourceIsSafeInternalError(t *testing.T) {
	code, message, status := classify(thumbnail.ErrNilSource)
	if code != "InternalError" || status != http.StatusInternalServerError {
		t.Fatalf("classify(nil source) = (%q, %q, %d), want InternalError/500", code, message, status)
	}
	if strings.Contains(message, "nil source reader") {
		t.Fatalf("message exposes implementation detail: %q", message)
	}
}

type nilThumbnailStorage struct {
	storage.Storage
	reader io.ReadCloser
	gets   atomic.Int32
}

func (s *nilThumbnailStorage) Get(context.Context, string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets.Add(1)
	return s.reader, storage.ObjectInfo{}, nil
}

func newNilThumbnailSourceREST(t *testing.T, reader io.ReadCloser) (*httptest.Server, *Handler, *nilThumbnailStorage, *thumbnail.Cache) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "nil-source.db"))
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
	store := &nilThumbnailStorage{Storage: realStore, reader: reader}
	cache := thumbnail.NewCache(1<<20, 0)
	h := NewHandler(service.NewFileService(store, repo, nil).WithDeleteFailOpen(true), nil).
		WithThumbnailCache(cache).
		WithThumbnailDecodeAdmission(thumbnail.NewDecodeAdmission(1))
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = repo.Close()
	})
	return srv, h, store, cache
}

func assertThumbnailAdmissionReusable(t *testing.T, admission *thumbnail.DecodeAdmission) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := admission.Acquire(ctx, "default")
	if err != nil {
		t.Fatalf("admission remained held: %v", err)
	}
	release()
}

func TestThumbnailGenericNilSourceReturnsStructured500AndReleasesAdmission(t *testing.T) {
	var typedNil *nilSniffReadCloser
	for _, tc := range []struct {
		name   string
		reader io.ReadCloser
	}{
		{name: "nil", reader: nil},
		{name: "typed nil", reader: typedNil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h, store, cache := newNilThumbnailSourceREST(t, tc.reader)
			u := srv.URL + "/v1/files/nil-source"
			data := pngBytes(t, 8, 8)
			if resp, _ := req(t, http.MethodPut, u, data, map[string]string{
				"Content-Type": "application/octet-stream",
			}); resp.StatusCode != http.StatusCreated {
				t.Fatalf("PUT status = %d, want %d", resp.StatusCode, http.StatusCreated)
			}
			resp, body := req(t, http.MethodGet, u+"/thumbnail", nil, nil)
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("GET status = %d, want 500 (body=%q)", resp.StatusCode, body)
			}
			if !bytes.Contains(body, []byte(`"code":"InternalError"`)) {
				t.Fatalf("body = %s, want InternalError", body)
			}
			if !bytes.Contains(body, []byte("thumbnail source unavailable")) {
				t.Fatalf("body = %s, want safe source message", body)
			}
			if bytes.Contains(body, []byte("nil source reader")) || bytes.Contains(body, []byte("typed nil source was")) {
				t.Fatalf("body exposes implementation details: %s", body)
			}
			if resp.Header.Get("Content-Type") == "image/jpeg" {
				t.Fatal("failure response advertised image/jpeg")
			}
			if cache.Len() != 0 || cache.Bytes() != 0 {
				t.Fatalf("nil-source failure populated cache: len=%d bytes=%d", cache.Len(), cache.Bytes())
			}
			if got := store.gets.Load(); got != 1 {
				t.Fatalf("storage Get count = %d, want 1", got)
			}
			assertThumbnailAdmissionReusable(t, h.thumbnailAdmission)
		})
	}
}

type deadlineSniffReadCloser struct {
	ctx     context.Context
	data    []byte
	started chan struct{}
	start   sync.Once
	calls   atomic.Int32
	closes  atomic.Int32
}

func (r *deadlineSniffReadCloser) Read(p []byte) (int, error) {
	if r.calls.Add(1) != 1 {
		return 0, errors.New("source read after prefix cancellation")
	}
	r.start.Do(func() { close(r.started) })
	<-r.ctx.Done()
	return copy(p, r.data[:2]), nil
}

func (r *deadlineSniffReadCloser) Close() error {
	r.closes.Add(1)
	return nil
}

type deadlineSniffStorage struct {
	storage.Storage
	source *deadlineSniffReadCloser
}

func (s *deadlineSniffStorage) GetGenerationBound(ctx context.Context, _ string, expected storage.ObjectInfo) (io.ReadCloser, storage.ObjectInfo, error) {
	s.source.ctx = ctx
	return s.source, expected, nil
}

func newDeadlineSniffREST(t *testing.T, payload []byte) (*httptest.Server, *Handler, *deadlineSniffStorage, *thumbnail.Cache) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "deadline-sniff.db"))
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
	store := &deadlineSniffStorage{
		Storage: realStore,
		source:  &deadlineSniffReadCloser{data: payload, started: make(chan struct{})},
	}
	cache := thumbnail.NewCache(1<<20, 0)
	h := NewHandler(service.NewFileService(store, repo, nil).WithDeleteFailOpen(true), nil).
		WithThumbnailCache(cache).
		WithThumbnailDecodeAdmission(thumbnail.NewDecodeAdmission(1))
	h.thumbnailTimeout = 40 * time.Millisecond
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = repo.Close()
	})
	return srv, h, store, cache
}

func jpegBytesForThumbnailTest(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buf.Bytes()
}

func TestThumbnailGenericSniffDeadlineReturns504WithoutCacheOrSecondRead(t *testing.T) {
	payload := jpegBytesForThumbnailTest(t, 8, 8)
	srv, h, store, cache := newDeadlineSniffREST(t, payload)
	u := srv.URL + "/v1/files/deadline.jpg"
	if resp, _ := req(t, http.MethodPut, u, payload, map[string]string{
		"Content-Type": "application/octet-stream",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	resp, body := req(t, http.MethodGet, u+"/thumbnail?w=32&h=32", nil, nil)
	select {
	case <-store.source.started:
	default:
		t.Fatal("generic source was not read")
	}
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("GET status = %d, want 504 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"Timeout"`)) {
		t.Fatalf("body = %s, want Timeout", body)
	}
	if resp.Header.Get("Content-Type") == "image/jpeg" {
		t.Fatal("deadline response advertised image/jpeg")
	}
	if img, format, err := image.Decode(bytes.NewReader(body)); err == nil && format == "jpeg" {
		t.Fatalf("deadline response contained JPEG bounds=%s", img.Bounds())
	}
	if got := store.source.calls.Load(); got != 1 {
		t.Fatalf("source read calls = %d, want 1", got)
	}
	if got := store.source.closes.Load(); got != 1 {
		t.Fatalf("source closes = %d, want 1", got)
	}
	if cache.Len() != 0 || cache.Bytes() != 0 {
		t.Fatalf("deadline failure populated cache: len=%d bytes=%d", cache.Len(), cache.Bytes())
	}
	assertThumbnailAdmissionReusable(t, h.thumbnailAdmission)
}
