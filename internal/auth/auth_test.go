package auth

import (
	"context"
	"strings"
	"testing"
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
