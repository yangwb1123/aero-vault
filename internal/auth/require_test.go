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

func TestRequire_AllowsAnonymousObjectReadAfterAuthAdmission(t *testing.T) {
	reg, err := Parse("reader:default:read")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	reg.WithAnonymousPublicRead(true)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if !IsAnonymous(r.Context()) {
			t.Fatal("anonymous marker was lost before Require")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := reg.Middleware()(reg.Require(ScopeRead)(next))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/files/public.txt", nil))

	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("anonymous object read status=%d called=%v, want %d/true", rec.Code, called, http.StatusNoContent)
	}
}

func TestRequire_AnonymousReadIsLimitedToObjectPaths(t *testing.T) {
	reg, err := Parse("reader:default:read")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	reg.WithAnonymousPublicRead(true)

	for _, path := range []string{"/v1/files", "/v1/files/public.txt/tags", "/v1/buckets", "/v1/search"} {
		t.Run(path, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
			h := reg.Middleware()(reg.Require(ScopeRead)(next))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if called || rec.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous path %s status=%d called=%v, want 401/false", path, rec.Code, called)
			}
		})
	}
}
