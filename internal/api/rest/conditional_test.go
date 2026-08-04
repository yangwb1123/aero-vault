package rest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newRESTTest(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h := NewHandler(service.NewFileService(store, repo, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv
}

func req(t *testing.T, method, url string, body []byte, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	rq, _ := http.NewRequest(method, url, rd)
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestConditionalGetIfNoneMatch(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/doc.txt"
	req(t, "PUT", u, []byte("hello"), nil)

	resp, _ := req(t, "GET", u, nil, nil)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on GET")
	}

	// Matching If-None-Match → 304, empty body.
	resp, body := req(t, "GET", u, nil, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match match: status=%d want 304", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Fatalf("304 should have empty body, got %q", body)
	}
	if resp.Header.Get("ETag") != etag {
		t.Fatalf("304 should echo ETag")
	}

	// Non-matching → 200 with body.
	resp, body = req(t, "GET", u, nil, map[string]string{"If-None-Match": `"deadbeef"`})
	if resp.StatusCode != 200 || string(body) != "hello" {
		t.Fatalf("non-matching INM: status=%d body=%q", resp.StatusCode, body)
	}

	// HEAD honours it too.
	resp, _ = req(t, "HEAD", u, nil, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("HEAD If-None-Match: status=%d want 304", resp.StatusCode)
	}
}

func TestConditionalGetIfModifiedSince(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/doc.txt"
	req(t, "PUT", u, []byte("hello"), nil)

	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	resp, _ := req(t, "GET", u, nil, map[string]string{"If-Modified-Since": future})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-Modified-Since future: status=%d want 304", resp.StatusCode)
	}

	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	resp, body := req(t, "GET", u, nil, map[string]string{"If-Modified-Since": past})
	if resp.StatusCode != 200 || string(body) != "hello" {
		t.Fatalf("If-Modified-Since past: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestConditionalGetIfMatch(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/doc.txt"
	req(t, "PUT", u, []byte("hello"), nil)

	resp, _ := req(t, "GET", u, nil, nil)
	etag := resp.Header.Get("ETag")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		resp, body := req(t, method, u, nil, map[string]string{
			"If-Match": `"definitely-wrong"`,
		})
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("%s wrong If-Match: status=%d want 412", method, resp.StatusCode)
		}
		if method == http.MethodGet && len(body) == 0 {
			t.Fatalf("%s 412 should include the REST error body", method)
		}

		resp, body = req(t, method, u, nil, map[string]string{"If-Match": etag})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s matching If-Match: status=%d want 200", method, resp.StatusCode)
		}
		if method == http.MethodGet && string(body) != "hello" {
			t.Fatalf("%s matching body=%q", method, body)
		}
	}
}

func TestConditionalGetIfUnmodifiedSince(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/doc.txt"
	req(t, "PUT", u, []byte("hello"), nil)

	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	resp, _ := req(t, "GET", u, nil, map[string]string{
		"If-Unmodified-Since": past,
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("past If-Unmodified-Since: status=%d want 412", resp.StatusCode)
	}

	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	resp, body := req(t, "GET", u, nil, map[string]string{
		"If-Unmodified-Since": future,
	})
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("future If-Unmodified-Since: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestPutIfNoneMatchStar(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/new.txt"

	// First create with If-None-Match:* succeeds.
	resp, _ := req(t, "PUT", u, []byte("v1"), map[string]string{"If-None-Match": "*"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create-only first PUT: status=%d want 201", resp.StatusCode)
	}
	// Second create-only PUT fails with 412.
	resp, _ = req(t, "PUT", u, []byte("v2"), map[string]string{"If-None-Match": "*"})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("create-only second PUT: status=%d want 412", resp.StatusCode)
	}
	// Content unchanged.
	_, body := req(t, "GET", u, nil, nil)
	if string(body) != "v1" {
		t.Fatalf("object should remain v1, got %q", body)
	}
}

func TestPutIfMatch(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/lock.txt"
	req(t, "PUT", u, []byte("v1"), nil)
	resp, _ := req(t, "GET", u, nil, nil)
	etag := resp.Header.Get("ETag")

	// If-Match with correct etag → overwrite ok.
	resp, _ = req(t, "PUT", u, []byte("v2"), map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("If-Match correct: status=%d want 201", resp.StatusCode)
	}
	// If-Match with stale etag → 412.
	resp, _ = req(t, "PUT", u, []byte("v3"), map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match stale: status=%d want 412", resp.StatusCode)
	}
	_, body := req(t, "GET", u, nil, nil)
	if string(body) != "v2" {
		t.Fatalf("object should be v2, got %q", body)
	}
}

func TestRESTRange(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/alpha.txt"
	req(t, "PUT", u, []byte("ABCDEFGHIJ"), nil)
	resp, body := req(t, "GET", u, nil, map[string]string{"Range": "bytes=2-4"})
	if resp.StatusCode != http.StatusPartialContent || string(body) != "CDE" {
		t.Fatalf("range: status=%d body=%q", resp.StatusCode, body)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 2-4/10" {
		t.Fatalf("content-range=%q", cr)
	}
}
