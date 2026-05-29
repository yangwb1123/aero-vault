package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- RequestID ---

func TestRequestID_GeneratesAndThreads(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if seen == "" {
		t.Fatal("RequestID middleware should populate request id in context")
	}
	if got := rec.Header().Get("X-Request-ID"); got != seen {
		t.Fatalf("response header X-Request-ID = %q, want %q (matches context)", got, seen)
	}
}

func TestRequestID_HonorsIncomingHeader(t *testing.T) {
	const incoming = "client-supplied-123"
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-ID", incoming)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != incoming {
		t.Fatalf("context request id = %q, want incoming %q", seen, incoming)
	}
	if got := rec.Header().Get("X-Request-ID"); got != incoming {
		t.Fatalf("echoed header = %q, want %q", got, incoming)
	}
}

func TestRequestIDFrom_EmptyContext(t *testing.T) {
	if got := RequestIDFrom(context.Background()); got != "" {
		t.Fatalf("RequestIDFrom(empty) = %q, want empty string", got)
	}
}

// --- Tenant ---

func TestTenant_FromHeader(t *testing.T) {
	var seen string
	h := Tenant(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = TenantFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(TenantHeader, "acme")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "acme" {
		t.Fatalf("tenant = %q, want acme", seen)
	}
}

func TestTenant_DefaultsWhenHeaderMissing(t *testing.T) {
	var seen string
	h := Tenant(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = TenantFrom(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if seen != "default" {
		t.Fatalf("tenant = %q, want default", seen)
	}
}

func TestTenantFrom_RoundTripAndDefault(t *testing.T) {
	// Empty context -> "default".
	if got := TenantFrom(context.Background()); got != "default" {
		t.Fatalf("TenantFrom(empty) = %q, want default", got)
	}
	// Explicit value round-trips.
	ctx := context.WithValue(context.Background(), ctxTenantID, "tenant-7")
	if got := TenantFrom(ctx); got != "tenant-7" {
		t.Fatalf("TenantFrom = %q, want tenant-7", got)
	}
}

func TestTenantHeaderConstant(t *testing.T) {
	if TenantHeader != "X-Aero-Tenant" {
		t.Fatalf("TenantHeader = %q, want X-Aero-Tenant", TenantHeader)
	}
}

// --- Recoverer ---

func TestRecoverer_TurnsPanicInto500(t *testing.T) {
	h := Recoverer(quietLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	// Must not propagate the panic.
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestRecoverer_PassesThroughNormalRequests(t *testing.T) {
	h := Recoverer(quietLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 (no panic path)", rec.Code)
	}
}

// --- AccessLog (statusWriter capture) ---

func TestAccessLog_PassesThroughStatusAndBody(t *testing.T) {
	h := AccessLog(quietLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q, want hello", rec.Body.String())
	}
}

// --- statusWriter directly ---

func TestStatusWriter_DefaultAndCapture(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

	n, err := sw.Write([]byte("abc"))
	if err != nil || n != 3 {
		t.Fatalf("Write = (%d,%v)", n, err)
	}
	// status defaults to 200 when WriteHeader not called explicitly.
	if sw.status != http.StatusOK {
		t.Fatalf("default status = %d, want 200", sw.status)
	}
	if sw.bytes != 3 {
		t.Fatalf("bytes = %d, want 3", sw.bytes)
	}

	sw.WriteHeader(http.StatusBadGateway)
	if sw.status != http.StatusBadGateway {
		t.Fatalf("status after WriteHeader = %d, want 502", sw.status)
	}
	// More writes accumulate.
	_, _ = sw.Write([]byte("de"))
	if sw.bytes != 5 {
		t.Fatalf("bytes = %d, want 5", sw.bytes)
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() { f.flushed = true }

func TestStatusWriter_FlushForwards(t *testing.T) {
	fr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	sw := &statusWriter{ResponseWriter: fr, status: http.StatusOK}
	sw.Flush()
	if !fr.flushed {
		t.Fatal("statusWriter.Flush should forward to underlying Flusher")
	}
}

func TestStatusWriter_FlushNoPanicWithoutFlusher(t *testing.T) {
	// httptest.ResponseRecorder implements Flusher, so use a bare writer that
	// does not. A plain struct wrapping the recorder via interface would still
	// expose Flush; instead wrap with a type that only implements the minimum.
	sw := &statusWriter{ResponseWriter: nonFlusher{httptest.NewRecorder()}, status: http.StatusOK}
	// Should be a no-op, not a panic.
	sw.Flush()
}

// nonFlusher hides the Flush method of the embedded recorder by only promoting
// the http.ResponseWriter interface methods.
type nonFlusher struct {
	w http.ResponseWriter
}

func (n nonFlusher) Header() http.Header         { return n.w.Header() }
func (n nonFlusher) Write(b []byte) (int, error) { return n.w.Write(b) }
func (n nonFlusher) WriteHeader(code int)        { n.w.WriteHeader(code) }

// --- Auth placeholder ---

func TestAuth_IsPassThrough(t *testing.T) {
	called := false
	h := Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("Auth placeholder should pass through; called=%v code=%d", called, rec.Code)
	}
}

// --- CORS ---

func TestCORS_DisabledWhenNoOrigins(t *testing.T) {
	h := CORS(CORSConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disabled CORS must not set Allow-Origin")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestCORS_AllowedOriginStampsHeaders(t *testing.T) {
	h := CORS(CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		ExposeHeaders:  []string{"ETag"},
		AllowCreds:     true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	hd := rec.Header()
	if hd.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("Allow-Origin = %q", hd.Get("Access-Control-Allow-Origin"))
	}
	if hd.Get("Vary") != "Origin" {
		t.Fatalf("Vary = %q, want Origin", hd.Get("Vary"))
	}
	if !strings.Contains(hd.Get("Access-Control-Allow-Methods"), "GET") {
		t.Fatalf("default methods missing GET: %q", hd.Get("Access-Control-Allow-Methods"))
	}
	if hd.Get("Access-Control-Expose-Headers") != "ETag" {
		t.Fatalf("Expose-Headers = %q", hd.Get("Access-Control-Expose-Headers"))
	}
	if hd.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("Allow-Credentials = %q, want true", hd.Get("Access-Control-Allow-Credentials"))
	}
	if hd.Get("Access-Control-Max-Age") != "600" {
		t.Fatalf("Max-Age = %q, want default 600", hd.Get("Access-Control-Max-Age"))
	}
}

func TestCORS_DisallowedOriginNoHeaders(t *testing.T) {
	h := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example.com"}})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://other.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed origin must not be reflected")
	}
}

func TestCORS_Wildcard(t *testing.T) {
	h := CORS(CORSConfig{AllowedOrigins: []string{"*"}})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://anything.dev")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://anything.dev" {
		t.Fatalf("wildcard should reflect any origin, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_PreflightShortCircuits(t *testing.T) {
	nextCalled := false
	h := CORS(CORSConfig{AllowedOrigins: []string{"*"}})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nextCalled = true }))
	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://anything.dev")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if nextCalled {
		t.Fatal("OPTIONS preflight must not call next handler")
	}
}

func TestCORS_OriginCaseInsensitive(t *testing.T) {
	h := CORS(CORSConfig{AllowedOrigins: []string{"https://App.Example.com"}})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://app.example.com") // different case
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("origin match should be case-insensitive, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestStrFromInt(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{600, "600"},
		{-42, "-42"},
		{1000000, "1000000"},
	}
	for _, c := range cases {
		if got := strFromInt(c.in); got != c.want {
			t.Fatalf("strFromInt(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
