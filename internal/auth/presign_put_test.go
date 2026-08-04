package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
)

const testPresignSecret = "0123456789abcdef0123456789abcdef"

func TestPutPresignerBindsMethodPathTenantAndExpiry(t *testing.T) {
	signer := NewPutPresigner(testPresignSecret)
	signed, err := signer.SignPut(
		"http://vault.example/v1/files/folder/a.txt",
		"acme",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("SignPut: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, signed, strings.NewReader("data"))
	key, err := signer.VerifyPut(req)
	if err != nil {
		t.Fatalf("VerifyPut: %v", err)
	}
	if key.Tenant != "acme" || !key.Has(ScopeWrite) {
		t.Fatalf("verified key = %#v", key)
	}

	t.Run("wrong method", func(t *testing.T) {
		bad := httptest.NewRequest(http.MethodGet, signed, nil)
		if _, err := signer.VerifyPut(bad); err == nil {
			t.Fatal("GET unexpectedly accepted")
		}
	})
	t.Run("changed path", func(t *testing.T) {
		bad := httptest.NewRequest(http.MethodPut, signed, nil)
		bad.URL.Path = "/v1/files/other.txt"
		if _, err := signer.VerifyPut(bad); err == nil {
			t.Fatal("changed path unexpectedly accepted")
		}
	})
	t.Run("changed tenant", func(t *testing.T) {
		bad := httptest.NewRequest(http.MethodPut, signed, nil)
		q := bad.URL.Query()
		q.Set(presignPutTenantKey, "other")
		bad.URL.RawQuery = q.Encode()
		if _, err := signer.VerifyPut(bad); err == nil {
			t.Fatal("changed tenant unexpectedly accepted")
		}
	})
	t.Run("expired", func(t *testing.T) {
		bad := httptest.NewRequest(http.MethodPut, signed, nil)
		q := bad.URL.Query()
		q.Set(presignPutExpiresKey, "1")
		q.Set(presignPutSignatureKey, signer.signature(bad.URL.EscapedPath(), "acme", 1))
		bad.URL.RawQuery = q.Encode()
		if _, err := signer.VerifyPut(bad); err == nil {
			t.Fatal("expired URL unexpectedly accepted")
		}
	})
}

func TestPutPresignerRejectsInvalidInputs(t *testing.T) {
	signer := NewPutPresigner(testPresignSecret)
	cases := []struct {
		name   string
		target string
		tenant string
		expiry time.Duration
	}{
		{"relative URL", "/v1/files/a.txt", "acme", time.Minute},
		{"empty tenant", "http://vault/v1/files/a.txt", "", time.Minute},
		{"zero expiry", "http://vault/v1/files/a.txt", "acme", 0},
		{"long expiry", "http://vault/v1/files/a.txt", "acme", 8 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := signer.SignPut(tc.target, tc.tenant, tc.expiry); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPutPresignerBindsGetCapability(t *testing.T) {
	signer := NewPutPresigner(testPresignSecret)
	signed, err := signer.SignGet(
		"http://vault.example/v1/files/folder/a%20file.txt",
		"acme",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("SignGet: %v", err)
	}
	if !strings.Contains(signed, presignOperationKey+"=get") {
		t.Fatalf("signed URL missing GET operation: %s", signed)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, signed, nil)
		key, verifyErr := signer.VerifyGet(req)
		if verifyErr != nil {
			t.Fatalf("VerifyGet(%s): %v", method, verifyErr)
		}
		if key.Tenant != "acme" || !key.Has(ScopeRead) {
			t.Fatalf("verified key = %#v", key)
		}
	}

	t.Run("wrong method", func(t *testing.T) {
		if _, err := signer.VerifyGet(httptest.NewRequest(http.MethodPut, signed, nil)); err == nil {
			t.Fatal("PUT unexpectedly accepted")
		}
	})
	t.Run("changed path", func(t *testing.T) {
		bad := httptest.NewRequest(http.MethodGet, signed, nil)
		bad.URL.Path = "/v1/files/other.txt"
		if _, err := signer.VerifyGet(bad); err == nil {
			t.Fatal("changed path unexpectedly accepted")
		}
	})
	t.Run("changed tenant", func(t *testing.T) {
		bad := httptest.NewRequest(http.MethodGet, signed, nil)
		q := bad.URL.Query()
		q.Set(presignPutTenantKey, "other")
		bad.URL.RawQuery = q.Encode()
		if _, err := signer.VerifyGet(bad); err == nil {
			t.Fatal("changed tenant unexpectedly accepted")
		}
	})
	t.Run("added query", func(t *testing.T) {
		bad := httptest.NewRequest(http.MethodGet, signed, nil)
		q := bad.URL.Query()
		q.Set("version", "another-version")
		bad.URL.RawQuery = q.Encode()
		if _, err := signer.VerifyGet(bad); err == nil {
			t.Fatal("added query unexpectedly accepted")
		}
	})
}

func TestPutPresignerMiddlewareAuthenticatesCapability(t *testing.T) {
	reg, err := Parse("ordinary:acme:read+write")
	if err != nil {
		t.Fatal(err)
	}
	signer := NewPutPresigner(testPresignSecret)
	reg.WithPutPresigner(signer)
	signed, err := signer.SignPut("http://vault/v1/files/a.txt", "acme", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := FromContext(r.Context())
		if !ok || key.Tenant != "acme" || r.Header.Get("X-Aero-Tenant") != "acme" {
			t.Errorf("capability context/header missing: key=%#v ok=%v", key, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	reg.Middleware()(next).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodPut, signed, strings.NewReader("data")),
	)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	conflict := httptest.NewRequest(http.MethodPut, signed, nil)
	conflict.Header.Set("X-Aero-Tenant", "other")
	rec = httptest.NewRecorder()
	reg.Middleware()(next).ServeHTTP(rec, conflict)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant conflict status=%d want 403", rec.Code)
	}
}

func TestPutPresignerMiddlewareAuthenticatesGetCapability(t *testing.T) {
	reg, err := Parse("ordinary:acme:read+write")
	if err != nil {
		t.Fatal(err)
	}
	signer := NewPutPresigner(testPresignSecret)
	reg.WithPutPresigner(signer)
	signed, err := signer.SignGet("http://vault/v1/files/folder/a.txt", "acme", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := FromContext(r.Context())
		principal, principalOK := access.PrincipalFrom(r.Context())
		if !ok || key.Tenant != "acme" || r.Header.Get("X-Aero-Tenant") != "acme" {
			t.Errorf("capability context/header missing: key=%#v ok=%v", key, ok)
		}
		if !principalOK || principal.Kind != access.PrincipalService || principal.Capability == nil ||
			principal.Capability.Key != "folder/a.txt" {
			t.Errorf("object capability missing: principal=%#v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	reg.Middleware()(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, signed, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	conflict := httptest.NewRequest(http.MethodGet, signed, nil)
	conflict.Header.Set("X-Aero-Tenant", "other")
	rec = httptest.NewRecorder()
	reg.Middleware()(next).ServeHTTP(rec, conflict)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant conflict status=%d want 403", rec.Code)
	}
}

func TestIsPresignedPut(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "http://vault/v1/files/a", nil)
	if IsPresignedPut(req) {
		t.Fatal("unsigned request reported as presigned")
	}
	q := url.Values{presignPutSignatureKey: []string{"sig"}}
	req.URL.RawQuery = q.Encode()
	if !IsPresignedPut(req) {
		t.Fatal("signed request not detected")
	}
	if IsPresignedGet(req) {
		t.Fatal("PUT capability reported as GET")
	}
	req.Method = http.MethodGet
	q.Set(presignOperationKey, "get")
	req.URL.RawQuery = q.Encode()
	if !IsPresignedGet(req) || IsPresignedPut(req) {
		t.Fatal("GET capability detection failed")
	}
}
