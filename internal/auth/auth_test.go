package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A record with an empty (or whitespace-only) scope segment yields a key with
// zero scopes, which would silently 403 on every route. Parse must reject it.
func TestParse_RejectsEmptyScope(t *testing.T) {
	for _, raw := range []string{
		"tok:acme:",  // trailing colon, no scope
		"tok:acme: ", // whitespace-only scope
		"tok:acme:+", // separator only, no scope names
	} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("Parse(%q): expected an error for a scopeless record", raw)
		}
	}
}

// A well-formed record still parses, and the scope-rejection must not fire for
// a record that does carry at least one scope.
func TestParse_AcceptsScopedRecord(t *testing.T) {
	reg, err := Parse("tok:acme:read+write")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	k, ok := reg.Lookup(context.Background(), "tok")
	if !ok || !k.Has(ScopeRead) || !k.Has(ScopeWrite) {
		t.Fatalf("scoped record should resolve with its scopes: %+v ok=%v", k, ok)
	}
}

// A persisted key whose ExpiresAt is not valid RFC3339 must fail closed: an
// unparseable expiry was previously ignored, treating the key as non-expiring.
func TestPersistedKey_MalformedExpiryFailsClosed(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("")
	fs := newFakeStore()
	reg.WithStore(fs)

	const tok = "bad-expiry"
	// Write the record directly with a garbage ExpiresAt (AddKey would relay it
	// to the store verbatim too, but this keeps the malformed value explicit).
	fs.m[HashToken(tok)] = PersistedKey{
		TokenHash: HashToken(tok),
		TenantID:  "acme",
		Scopes:    "read",
		ExpiresAt: "not-a-timestamp",
	}
	if _, ok := reg.Lookup(ctx, tok); ok {
		t.Fatal("a key with an unparseable ExpiresAt must not resolve (fail closed)")
	}
}

// The malformed-expiry guard must not break the empty-expiry path: ExpiresAt ""
// means no expiry and the key resolves normally.
func TestPersistedKey_EmptyExpiryResolves(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("")
	fs := newFakeStore()
	reg.WithStore(fs)

	const tok = "no-expiry"
	fs.m[HashToken(tok)] = PersistedKey{
		TokenHash: HashToken(tok),
		TenantID:  "acme",
		Scopes:    "read",
		ExpiresAt: "",
	}
	if _, ok := reg.Lookup(ctx, tok); !ok {
		t.Fatal("a key with no expiry should resolve")
	}
}

// Guard that the rejection message names the offending record (matches the
// existing AUTH_KEYS error-wrapping style).
func TestParse_EmptyScopeErrorNamesRecord(t *testing.T) {
	_, err := Parse("tok:acme:")
	if err == nil || !strings.Contains(err.Error(), "tok:acme:") {
		t.Fatalf("error should name the offending record, got %v", err)
	}
}

func TestParse_MalformedConfigReturnsFailClosedRegistry(t *testing.T) {
	reg, err := Parse("valid:acme:read,malformed")
	if err == nil {
		t.Fatal("malformed AUTH_KEYS must return an error")
	}
	if reg == nil {
		t.Fatal("malformed AUTH_KEYS must return a non-nil registry")
	}
	if !reg.Enabled() {
		t.Fatal("malformed AUTH_KEYS registry must remain enabled")
	}
	if _, ok := reg.Lookup(context.Background(), "valid"); ok {
		t.Fatal("partially parsed credentials must be discarded")
	}

	called := false
	protected := reg.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/files/secret.txt", nil)
	req.Header.Set("Authorization", "Bearer valid")
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("malformed config request status=%d want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("fail-closed registry must not invoke the protected handler")
	}
}

func TestParse_NonEmptyConfigWithoutRecordsFailsClosed(t *testing.T) {
	reg, err := Parse(",,,")
	if err == nil {
		t.Fatal("non-empty AUTH_KEYS without records must return an error")
	}
	if reg == nil || !reg.Enabled() {
		t.Fatalf("registry must be non-nil and enabled, got %#v", reg)
	}
}

func TestRegistryEnabledWithJWKSOnly(t *testing.T) {
	reg, err := Parse("")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	reg.WithJWKS("https://issuer.example/.well-known/jwks.json", time.Minute, "issuer")
	if !reg.Enabled() {
		t.Fatal("JWKS-only authentication must enable the registry")
	}
}
