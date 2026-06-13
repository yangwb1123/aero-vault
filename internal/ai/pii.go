package ai

import (
	"regexp"
	"strings"
)

// digitsOnly returns s with all non-digit characters removed.
func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// luhn returns true when the digit string s passes the Luhn check.
// Returns false if the number of digits is outside the 13–19 range.
func luhn(s string) bool {
	digits := digitsOnly(s)
	n := len(digits)
	if n < 13 || n > 19 {
		return false
	}
	sum := 0
	parity := n % 2
	for i, ch := range digits {
		d := int(ch - '0')
		if i%2 == parity {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// PIIDetector flags common personally-identifiable patterns. Hits are
// returned both as a count map and as a redacted copy of the text.
//
// Scope: email, phone (US/intl loose), credit card (Luhn-ish 13-19 digits),
// SSN (US 9-digit). For more complete coverage (passport, driver license,
// IBAN, regional ID), back this with Presidio or a hosted classifier.
type PIIDetector struct {
	rules []piiRule
}

type piiRule struct {
	kind string
	re   *regexp.Regexp
	repl string
}

func NewPIIDetector() *PIIDetector {
	return &PIIDetector{
		rules: []piiRule{
			{kind: "email", re: regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`), repl: "[REDACTED-EMAIL]"},
			{kind: "phone", re: regexp.MustCompile(`(?:\+\d{1,3}[\s\-]?)?(?:\(?\d{2,4}\)?[\s\-]?){2,4}\d{2,4}`), repl: "[REDACTED-PHONE]"},
			{kind: "credit_card", re: regexp.MustCompile(`\b(?:\d[ \-]?){13,19}\b`), repl: "[REDACTED-CC]"},
			{kind: "ssn", re: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), repl: "[REDACTED-SSN]"},
			{kind: "ip_v4", re: regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), repl: "[REDACTED-IP]"},
		},
	}
}

// Scan reports counts per kind without modifying the text.
func (p *PIIDetector) Scan(text string) map[string]int {
	out := map[string]int{}
	for _, r := range p.rules {
		m := r.re.FindAllString(text, -1)
		if r.kind == "credit_card" {
			valid := m[:0]
			for _, match := range m {
				if luhn(match) {
					valid = append(valid, match)
				}
			}
			m = valid
		}
		if len(m) > 0 {
			out[r.kind] = len(m)
		}
	}
	return out
}

// Redact returns the text with every match replaced by its kind-specific
// placeholder. The optional pickKinds argument restricts which rules apply;
// passing nil means redact everything.
func (p *PIIDetector) Redact(text string, pickKinds map[string]bool) string {
	for _, r := range p.rules {
		if pickKinds != nil && !pickKinds[r.kind] {
			continue
		}
		if r.kind == "credit_card" {
			repl := r.repl
			text = r.re.ReplaceAllStringFunc(text, func(match string) string {
				if luhn(match) {
					return repl
				}
				return match
			})
			continue
		}
		text = r.re.ReplaceAllString(text, r.repl)
	}
	return text
}

// MapPII produces a "kind=count" string suitable as metadata.
func MapPII(scan map[string]int) string {
	if len(scan) == 0 {
		return ""
	}
	parts := make([]string, 0, len(scan))
	for k, v := range scan {
		parts = append(parts, k+"="+strings.Repeat("0", 0)+itoa(v))
	}
	return strings.Join(parts, ",")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	var b [10]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b[i:])
}
