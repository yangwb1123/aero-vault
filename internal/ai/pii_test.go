package ai

import (
	"strings"
	"testing"
)

func TestLuhn(t *testing.T) {
	// Valid Visa number
	if !luhn("4532015112830366") {
		t.Error("expected luhn(4532015112830366) == true")
	}
	// Unix timestamp – 16 digits, fails Luhn
	if luhn("1700000000000000") {
		t.Error("expected luhn(1700000000000000) == false")
	}
	// Plain sequence – 16 digits, fails Luhn
	if luhn("1234567890123456") {
		t.Error("expected luhn(1234567890123456) == false")
	}
	// Too short
	if luhn("123456789012") {
		t.Error("expected luhn(123456789012) == false (too short)")
	}
	// Too long
	if luhn("12345678901234567890") {
		t.Error("expected luhn(12345678901234567890) == false (too long)")
	}
}

func TestPIIDetector_CreditCard_Scan(t *testing.T) {
	d := NewPIIDetector()

	// Valid Visa should be detected as credit_card
	counts := d.Scan("Card: 4532015112830366")
	if counts["credit_card"] != 1 {
		t.Errorf("Scan: expected 1 credit_card hit for valid Visa, got %d (full map: %v)", counts["credit_card"], counts)
	}

	// Unix timestamp must NOT be flagged as credit_card
	counts = d.Scan("ts=1700000000000000")
	if counts["credit_card"] != 0 {
		t.Errorf("Scan: Unix timestamp should not be flagged as credit_card, got %d", counts["credit_card"])
	}

	// Plain failing-Luhn sequence must NOT be flagged as credit_card
	counts = d.Scan("id=1234567890123456")
	if counts["credit_card"] != 0 {
		t.Errorf("Scan: plain digit sequence should not be flagged as credit_card, got %d", counts["credit_card"])
	}
}

func TestPIIDetector_Redact_CreditCard(t *testing.T) {
	d := NewPIIDetector()

	// A valid Visa number should be redacted (either as CC or phone — both are PII).
	out := d.Redact("Card: 4532015112830366", nil)
	if strings.Contains(out, "4532015112830366") {
		t.Errorf("Redact: valid Visa should be redacted, got %q", out)
	}

	// A non-Luhn sequence must NOT be replaced with [REDACTED-CC].
	// (Phone may still match it, but the CC-specific placeholder must not appear.)
	out = d.Redact("id=1234567890123456", nil)
	if strings.Contains(out, "[REDACTED-CC]") {
		t.Errorf("Redact: failing-Luhn sequence should not produce [REDACTED-CC], got %q", out)
	}

	// A Unix timestamp must NOT be replaced with [REDACTED-CC].
	out = d.Redact("ts=1700000000000000", nil)
	if strings.Contains(out, "[REDACTED-CC]") {
		t.Errorf("Redact: timestamp should not produce [REDACTED-CC], got %q", out)
	}
}

func TestPIIDetector_OtherRulesUnaffected(t *testing.T) {
	d := NewPIIDetector()

	text := "email: user@example.com ssn: 123-45-6789 ip: 192.168.1.1"
	counts := d.Scan(text)
	if counts["email"] != 1 {
		t.Errorf("expected 1 email, got %d", counts["email"])
	}
	if counts["ssn"] != 1 {
		t.Errorf("expected 1 ssn, got %d", counts["ssn"])
	}
	if counts["ip_v4"] != 1 {
		t.Errorf("expected 1 ip_v4, got %d", counts["ip_v4"])
	}
}
