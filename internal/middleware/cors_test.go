package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCORS_PreflightDisallowedOriginRejected verifies that an OPTIONS preflight
// from a present-but-disallowed Origin does NOT return a misleading 204 without
// Access-Control-Allow-Origin. It must be rejected (403) and must not reach the
// next handler. Regression test: the handler previously short-circuited every
// OPTIONS with 204 regardless of whether CORS headers were set.
func TestCORS_PreflightDisallowedOriginRejected(t *testing.T) {
	nextCalled := false
	h := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example.com"}})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nextCalled = true }))

	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatalf("preflight from disallowed origin must not return 204; got %d", rec.Code)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preflight from disallowed origin = %d, want 403", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin must not receive Access-Control-Allow-Origin, got %q",
			rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if nextCalled {
		t.Fatal("rejected preflight must not call the next handler")
	}
}

// TestCORS_PreflightAllowedOriginStillSucceeds confirms the allowed-origin
// preflight path still short-circuits with 204 and stamps Allow-Origin.
func TestCORS_PreflightAllowedOriginStillSucceeds(t *testing.T) {
	h := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example.com"}})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("OPTIONS preflight must not call next handler")
		}))

	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("allowed-origin preflight = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("allowed-origin preflight should set Allow-Origin, got %q",
			rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

// TestCORS_PreflightNoOriginStillSucceeds confirms a same-origin/non-browser
// OPTIONS with no Origin header still short-circuits with 204 (not a CORS
// preflight, so no rejection).
func TestCORS_PreflightNoOriginStillSucceeds(t *testing.T) {
	h := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example.com"}})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("OPTIONS must not call next handler")
		}))

	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	// no Origin header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("no-Origin OPTIONS = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("no-Origin OPTIONS must not set Allow-Origin, got %q",
			rec.Header().Get("Access-Control-Allow-Origin"))
	}
}
