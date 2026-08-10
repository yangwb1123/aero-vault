package repository_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// factIDPattern is the campaign's ID shape: 32 lowercase hex chars
// (SHA-256 hex digest truncated to 128 bits).
var factIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestDeterministicFactID_FormatAndDeterminism(t *testing.T) {
	at := time.Date(2026, 8, 8, 1, 17, 41, 123456789, time.UTC)
	first := repository.DeterministicFactID(
		"aero-vault.abc123", "acme", "tenant.status", "admin", 42, at)
	second := repository.DeterministicFactID(
		"aero-vault.abc123", "acme", "tenant.status", "admin", 42, at)
	if first != second {
		t.Fatalf("deterministic ID drifted: %q != %q", first, second)
	}
	if !factIDPattern.MatchString(first) {
		t.Fatalf("ID %q does not match %s", first, factIDPattern)
	}
	// Same instant in a different location must yield the same bucket.
	otherZone := at.In(time.FixedZone("CST", 8*3600))
	third := repository.DeterministicFactID(
		"aero-vault.abc123", "acme", "tenant.status", "admin", 42, otherZone)
	if third != first {
		t.Fatalf("timezone variant drifted: %q != %q", third, first)
	}
}

func TestDeterministicFactID_InputSensitivity(t *testing.T) {
	at := time.Date(2026, 8, 8, 1, 17, 41, 0, time.UTC)
	base := repository.DeterministicFactID("s", "t", "e", "k", 7, at)
	mutate := func(source, tenant, eventType, originKind string, originID int64, occurred time.Time) {
		t.Helper()
		got := repository.DeterministicFactID(source, tenant, eventType, originKind, originID, occurred)
		if got == base {
			t.Fatalf("ID unchanged by input mutation (%q,%q,%q,%q,%d,%v)", source, tenant, eventType, originKind, originID, occurred)
		}
	}
	mutate("S", "t", "e", "k", 7, at)
	mutate("s", "T", "e", "k", 7, at)
	mutate("s", "t", "E", "k", 7, at)
	mutate("s", "t", "e", "K", 7, at)
	mutate("s", "t", "e", "k", 8, at)
	mutate("s", "t", "e", "k", 7, at.Add(time.Second))
}

func TestDeterministicFactID_SecondBucket(t *testing.T) {
	// Same whole second (different ns) → identical bucket → identical ID.
	base := time.Date(2026, 8, 8, 1, 17, 41, 0, time.UTC)
	sameSecond := repository.DeterministicFactID("s", "t", "e", "k", 1, base.Add(999*time.Millisecond))
	if sameSecond != repository.DeterministicFactID("s", "t", "e", "k", 1, base) {
		t.Fatal("same-second IDs differ")
	}
	// Bucket boundary: one ns across the second flips the bucket.
	nextSecond := repository.DeterministicFactID("s", "t", "e", "k", 1, base.Add(time.Second))
	if sameSecond == nextSecond {
		t.Fatal("cross-second IDs are equal")
	}
	// Zero time truncates to the epoch bucket (deterministic, valid output).
	zero := repository.DeterministicFactID("s", "t", "e", "k", 1, time.Time{})
	if !factIDPattern.MatchString(zero) {
		t.Fatalf("zero-time ID %q does not match %s", zero, factIDPattern)
	}
}

// TestDeterministicFactID_GoldenValue pins the formula absolutely (F2): every
// other test recomputes via the production function, so a consistent formula
// change (hash algorithm, truncation length, separator, field order, bucket
// semantics) would be invisible to them. These literals were captured by
// executing the production function at HEAD 15763e2 (v2 design §3.1) — any
// drift fails loudly. B′ (same whole second as A, different ns) pins the
// second-bucket truncation.
func TestDeterministicFactID_GoldenValue(t *testing.T) {
	cases := []struct {
		name                            string
		source, tenant, eventType, kind string
		originID                        int64
		at                              time.Time
		want                            string
	}{
		{"A", "aero-vault.abc123", "acme", "tenant.status", "admin", 42,
			time.Date(2026, 8, 8, 1, 17, 41, 123456789, time.UTC), "efb5b5b734546a54aa21f5f7949ef896"},
		{"B", "aero-vault.abc123", "acme", "tenant.status", "admin", 42,
			time.Date(2026, 8, 8, 1, 17, 42, 123456789, time.UTC), "c6e9cdbbbe8a7d15a31d2a03f5fa2fbc"},
		{"Bprime", "aero-vault.abc123", "acme", "tenant.status", "admin", 42,
			time.Date(2026, 8, 8, 1, 17, 41, 999999999, time.UTC), "efb5b5b734546a54aa21f5f7949ef896"},
		{"C", "", "", "", "", 0,
			time.Time{}, "7a13533df046f5ca96da3f9e8b6c0c7d"},
	}
	for _, c := range cases {
		got := repository.DeterministicFactID(c.source, c.tenant, c.eventType, c.kind,
			c.originID, c.at)
		if got != c.want {
			t.Fatalf("%s: DeterministicFactID() = %q, want %q (formula drift)", c.name, got, c.want)
		}
		if !factIDPattern.MatchString(got) {
			t.Fatalf("%s: ID %q does not match %s", c.name, got, factIDPattern)
		}
	}
}

func TestDeterministicFactID_EdgeInputs(t *testing.T) {
	at := time.Date(2026, 8, 8, 1, 17, 41, 0, time.UTC)
	// Empty fields, NUL-like content and pipes must still frame unambiguously
	// (deterministic, valid 32-hex) — validation of legal values is upstream.
	for _, args := range [][6]any{
		{"", "", "", "", int64(0), at},
		{"a|b", "c|d", "e|f", "g|h", int64(3), at},
		{"x\x00y", "t", "e", "k", int64(9), at},
		{"aero-vault.+/=", "tenant.with:._-chars", "file.deleted", "file", int64(1234567890), at},
	} {
		id := repository.DeterministicFactID(args[0].(string), args[1].(string),
			args[2].(string), args[3].(string), args[4].(int64), args[5].(time.Time))
		if !factIDPattern.MatchString(id) {
			t.Fatalf("edge-input ID %q does not match %s", id, factIDPattern)
		}
	}
}
