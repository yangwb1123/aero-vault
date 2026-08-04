package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
)

func TestMiddlewareWebDAVMethodScopes(t *testing.T) {
	reg, err := Parse("reader:default:read,writer:default:write")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}

	tests := []struct {
		name       string
		method     string
		token      string
		wantStatus int
		wantCalled bool
	}{
		{name: "propfind accepts read", method: "PROPFIND", token: "reader", wantStatus: http.StatusNoContent, wantCalled: true},
		{name: "proppatch rejects read", method: "PROPPATCH", token: "reader", wantStatus: http.StatusForbidden},
		{name: "proppatch accepts write", method: "PROPPATCH", token: "writer", wantStatus: http.StatusNoContent, wantCalled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(tt.method, "/dav/object", nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rec := httptest.NewRecorder()

			reg.Middleware()(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d want %d", rec.Code, tt.wantStatus)
			}
			if called != tt.wantCalled {
				t.Fatalf("handler called=%v want %v", called, tt.wantCalled)
			}
		})
	}
}

func TestMiddlewarePublicCapabilitiesAreAnonymousButNotBypassed(t *testing.T) {
	reg, err := Parse("reader:default:read")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := access.PrincipalFrom(r.Context())
		if !ok || principal.Kind != access.PrincipalAnonymous {
			t.Fatalf("principal=%+v ok=%v, want anonymous", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	for _, path := range []string{"/share/token", "/public/assets/blog/hero.jpg"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, path, nil)
			reg.Middleware()(next).ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("%s %s status=%d want %d", method, path, rec.Code, http.StatusNoContent)
			}
		}
	}

	rec := httptest.NewRecorder()
	reg.Middleware()(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/share/token", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST share status=%d want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestExtractTokenAcceptsCaseInsensitiveAuthorizationSchemes(t *testing.T) {
	for _, value := range []string{
		"Bearer token",
		"bearer token",
		"BEARER token",
		"bEaReR token",
		"ApiKey token",
		"APIKEY token",
		"apikey token",
	} {
		t.Run(value, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", value)
			if got := extractToken(req); got != "token" {
				t.Fatalf("extractToken(%q)=%q, want token", value, got)
			}
		})
	}
}

func TestExtractTokenRejectsMalformedAuthorization(t *testing.T) {
	for _, value := range []string{
		"Bearer",
		"Bearer token extra",
		"Basic token",
	} {
		t.Run(value, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", value)
			if got := extractToken(req); got != "" {
				t.Fatalf("extractToken(%q)=%q, want empty", value, got)
			}
		})
	}
}

func TestMiddlewareBypassesPublicUIPaths(t *testing.T) {
	reg, err := Parse("reader:default:read")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	for _, path := range []string{"/", "/favicon.ico"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			reg.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusFound)
			})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusFound {
				t.Fatalf("status=%d want %d", rec.Code, http.StatusFound)
			}
		})
	}
}
