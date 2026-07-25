package middleware

import (
	"bytes"
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
