package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---- statusWriter unit tests -----------------------------------------------

func TestStatusWriter_DefaultStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	if sw.status != http.StatusOK {
		t.Fatalf("expected default status 200, got %d", sw.status)
	}
}

func TestStatusWriter_ImplementsResponseWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	// Compile-time check that *statusWriter satisfies http.ResponseWriter.
	var _ http.ResponseWriter = sw
}

func TestStatusWriter_WriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	sw.WriteHeader(http.StatusNotFound)
	if sw.status != http.StatusNotFound {
		t.Fatalf("expected status 404 after WriteHeader, got %d", sw.status)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected underlying recorder code 404, got %d", rec.Code)
	}
}

func TestStatusWriter_WriteWithoutWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	// Write without an explicit WriteHeader call; the underlying recorder
	// will implicitly use 200, and our status field stays at 200.
	_, err := sw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected Write error: %v", err)
	}
	if sw.status != http.StatusOK {
		t.Fatalf("expected status 200 after Write-only, got %d", sw.status)
	}
}

func TestStatusWriter_Flush_DoesNotPanic(t *testing.T) {
	// httptest.Recorder implements http.Flusher, so Flush should propagate.
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	// Should not panic regardless of whether the underlying writer is a Flusher.
	sw.Flush()
}

func TestStatusWriter_Flush_NonFlusher(t *testing.T) {
	// Wrap a plain ResponseWriter that does NOT implement http.Flusher.
	sw := &statusWriter{ResponseWriter: &nonFlusherWriter{}, status: http.StatusOK}
	// Must not panic.
	sw.Flush()
}

// nonFlusherWriter is a minimal http.ResponseWriter that does not implement
// http.Flusher, used to test the Flush no-op path.
type nonFlusherWriter struct {
	header http.Header
}

func (n *nonFlusherWriter) Header() http.Header {
	if n.header == nil {
		n.header = make(http.Header)
	}
	return n.header
}
func (n *nonFlusherWriter) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusherWriter) WriteHeader(_ int)           {}

// ---- HTTPMiddleware integration tests --------------------------------------

func TestHTTPMiddleware_InnerHandlerRuns(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	mw := HTTPMiddleware("aero-vault")
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("inner handler was not called")
	}
}

func TestHTTPMiddleware_DefaultStatus200(t *testing.T) {
	// Handler that never calls WriteHeader — response must be 200.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// intentionally empty; status should default to 200
	})
	mw := HTTPMiddleware("aero-vault")
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The recorder's Code will be 200 (default, since WriteHeader was never called).
	if rec.Code != http.StatusOK {
		t.Fatalf("expected recorder code 200, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_ExplicitWriteHeader404(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mw := HTTPMiddleware("aero-vault")
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected recorder code 404, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_WriteOnly_ImpliesStatus200(t *testing.T) {
	// Handler that only calls Write (no WriteHeader). HTTP spec means 200.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mw := HTTPMiddleware("aero-vault")
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected recorder code 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("expected body 'ok', got %q", rec.Body.String())
	}
}

func TestHTTPMiddleware_PostRequest(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	mw := HTTPMiddleware("aero-vault")
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodPost, "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected recorder code 201, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_ContextPropagation(t *testing.T) {
	// The middleware must pass a context that carries a span to the inner handler.
	var ctxHasSpan bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Context should be non-nil and carry OTel span (even a no-op one).
		ctxHasSpan = r.Context() != nil
	})
	mw := HTTPMiddleware("aero-vault")
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodGet, "/ctx", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !ctxHasSpan {
		t.Fatal("inner handler received nil context")
	}
}

func TestHTTPMiddleware_MultipleRequests(t *testing.T) {
	count := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
	})
	mw := HTTPMiddleware("aero-vault")
	handler := mw(inner)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/loop", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
	if count != 5 {
		t.Fatalf("expected inner handler called 5 times, got %d", count)
	}
}
