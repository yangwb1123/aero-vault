package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Require must enforce scopes under a JWT-only (or SigV4/store-only) config, where
// the env-keys-only `enabled` field is false but Enabled() is true. Previously it
// checked `enabled` and so passed unauthenticated requests straight through.
func TestRequire_EnforcesUnderJWTOnlyConfig(t *testing.T) {
	reg, _ := Parse("") // no env keys → enabled == false
	reg.WithJWT("test-secret")

	called := false
	h := reg.Require(ScopeWrite)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if called || rec.Code == http.StatusOK {
		t.Fatalf("Require under JWT-only config must reject the unauthenticated request, got status %d called=%v", rec.Code, called)
	}
}

// With no auth configured at all, Require stays a pass-through (MVP posture).
func TestRequire_PassThroughWhenDisabled(t *testing.T) {
	reg, _ := Parse("")
	called := false
	h := reg.Require(ScopeWrite)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Fatal("Require with auth disabled should pass through")
	}
}
