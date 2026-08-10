package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- MaxBodySize -------------------------------------------------------------

func TestMaxBodySize_DisabledWhenZero(t *testing.T) {
	body := []byte("hello world")
	h := MaxBodySize(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != string(body) {
			t.Fatalf("body = %q, want %q", string(b), string(body))
		}
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestMaxBodySize_UnderLimit(t *testing.T) {
	body := []byte("short")
	h := MaxBodySize(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != string(body) {
			t.Fatalf("body = %q, want %q", string(b), string(body))
		}
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("under-limit = %d, want 200", rec.Code)
	}
}

func TestMaxBodySize_ExceedsLimit(t *testing.T) {
	h := MaxBodySize(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not be called when body exceeds limit")
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte("too long body")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit = %d, want 413", rec.Code)
	}
}

func TestMaxBodySize_ExceedsViaContentLength(t *testing.T) {
	h := MaxBodySize(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not be called")
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte("hello world this is long")))
	req.ContentLength = 50 // lie about length; should be caught early
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit via Content-Length = %d, want 413", rec.Code)
	}
}

func TestMaxBodySize_GETNotAffected(t *testing.T) {
	// GET requests should not be affected by body size limit.
	h := MaxBodySize(1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rec.Code)
	}
}

// chunkedRequest builds a request whose body carries unknown length
// (ContentLength == -1, Transfer-Encoding: chunked) so the MaxBodySize
// early-reject path is skipped and the body goes through the wrapping
// reader — httptest.NewRequest would otherwise set a known Content-Length.
func chunkedRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	return req
}

// AC-1: an unknown-length (chunked) body that exceeds maxBytes must surface
// a distinguishable error (ErrBodyTooLarge), never a clean EOF — a clean EOF
// would let the adapter store a silently truncated object.
func TestMaxBodySize_ChunkedOversizeReturnsErrBodyTooLarge(t *testing.T) {
	var gotErr error
	h := MaxBodySize(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotErr = io.ReadAll(r.Body)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chunkedRequest(t, []byte("too long body"))) // 12 bytes > 5
	if gotErr == nil {
		t.Fatal("ReadAll returned nil error — truncation was silent")
	}
	if !errors.Is(gotErr, ErrBodyTooLarge) {
		t.Fatalf("error = %v, want errors.Is(err, ErrBodyTooLarge)", gotErr)
	}
	if errors.Is(gotErr, io.EOF) {
		t.Fatalf("error = %v must not satisfy errors.Is(err, io.EOF) — truncation must not look like a clean end", gotErr)
	}
}

// AC-2: no off-by-one. A body that ends exactly at maxBytes is fully read
// with a clean end; a body one byte over fails on the read after the limit.
func TestMaxBodySize_ChunkedExactLimitNoOffByOne(t *testing.T) {
	t.Run("exactly-at-limit reads fully, clean EOF", func(t *testing.T) {
		exact := bytes.Repeat([]byte("x"), 1024)
		var got []byte
		var gotErr error
		h := MaxBodySize(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, gotErr = io.ReadAll(r.Body)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, chunkedRequest(t, exact))
		if gotErr != nil {
			t.Fatalf("exactly-at-limit read: unexpected error %v", gotErr)
		}
		if !bytes.Equal(got, exact) {
			t.Fatalf("body = %d bytes, want 1024 bytes identical to input", len(got))
		}
	})
	t.Run("one byte over fails on the read after the limit", func(t *testing.T) {
		var firstN int
		var firstErr, nextErr error
		h := MaxBodySize(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 1024)
			firstN, firstErr = r.Body.Read(buf)
			if firstErr != nil {
				return
			}
			var one [1]byte
			_, nextErr = r.Body.Read(one[:])
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, chunkedRequest(t, bytes.Repeat([]byte("y"), 1025)))
		if firstErr != nil {
			t.Fatalf("first 1024-byte read: unexpected error %v", firstErr)
		}
		if firstN != 1024 {
			t.Fatalf("first read = %d bytes, want 1024", firstN)
		}
		if !errors.Is(nextErr, ErrBodyTooLarge) {
			t.Fatalf("read after limit: err = %v, want errors.Is(err, ErrBodyTooLarge)", nextErr)
		}
	})
}

// Control: chunked body under the limit passes through byte-identical
// (locks in no regression on normal chunked uploads).
func TestMaxBodySize_ChunkedUnderLimit(t *testing.T) {
	body := []byte("short")
	var got []byte
	var gotErr error
	h := MaxBodySize(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotErr = io.ReadAll(r.Body)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chunkedRequest(t, body))
	if gotErr != nil {
		t.Fatalf("under-limit chunked read: unexpected error %v", gotErr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", string(got), string(body))
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("under-limit chunked = %d, want 200", rec.Code)
	}
}

// --- SecureHeaders -----------------------------------------------------------

func TestSecureHeaders_SetsHeaders(t *testing.T) {
	h := SecureHeaders()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	expected := map[string]string{
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
	}
	for k, v := range expected {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %q = %q, want %q", k, got, v)
		}
	}
	// Permissions-Policy should be set.
	if got := rec.Header().Get("Permissions-Policy"); got == "" {
		t.Error("Permissions-Policy header must be set")
	}
}

func TestSecureHeaders_DoesNotOverrideExplicit(t *testing.T) {
	// Handler sets its own header; secure headers should be set first but not
	// override explicit downstream values.
	const customXFO = "SAMEORIGIN"
	h := SecureHeaders()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", customXFO)
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	// The last writer wins. Our middleware sets it first, then the handler
	// overrides. This test documents the behavior.
	if got := rec.Header().Get("X-Frame-Options"); got != customXFO {
		t.Fatalf("X-Frame-Options = %q, want handler value %q", got, customXFO)
	}
}

// --- EnforceContentType ------------------------------------------------------

func TestEnforceContentType_AllowsMatching(t *testing.T) {
	h := EnforceContentType("application/json")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("matching content-type = %d, want 200", rec.Code)
	}
}

func TestEnforceContentType_RejectsWrong(t *testing.T) {
	h := EnforceContentType("application/json")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not be called")
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`<xml/>`))
	req.Header.Set("Content-Type", "application/xml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content-type = %d, want 415", rec.Code)
	}
}

func TestEnforceContentType_SkipsGET(t *testing.T) {
	h := EnforceContentType("application/json")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET must be allowed without content-type = %d, want 200", rec.Code)
	}
}

func TestEnforceContentType_RejectsNoContentType(t *testing.T) {
	h := EnforceContentType("application/json")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not be called")
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	// No Content-Type header.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("no content-type = %d, want 415", rec.Code)
	}
}

func TestEnforceContentType_EmptyExpectedAlwaysPasses(t *testing.T) {
	h := EnforceContentType("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty expected = %d, want 200", rec.Code)
	}
}
