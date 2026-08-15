package thumbnail

import "testing"

// TestContentMD5ETag pins the admission shape gate (R1/R4): only exactly-32-
// lowercase-hex ETags are content-derived and may seed a CacheKey. The table
// covers the multipart "<md5>-<n>" shape, lengths 31/33/36, uppercase hex,
// empty, quoted, and the three edge rows QA flagged (32-char with dash,
// 32-char with a non-hex lowercase letter, 32-char mixed case) — a future
// refactor of the scan loop must not silently admit any of them.
func TestContentMD5ETag(t *testing.T) {
	tests := []struct {
		name string
		etag string
		want bool
	}{
		{"32 lowercase hex admits", "0123456789abcdef0123456789abcdef", true},
		{"multipart dash rejects", "abc123def4567890abcdef1234567890-3", false},
		{"36-char hex rejects", "0123456789abcdef0123456789abcdef0123", false},
		{"31 chars rejects", "0123456789abcdef0123456789abcde", false},
		{"33 chars rejects", "0123456789abcdef0123456789abcdef0", false},
		{"uppercase hex rejects", "0123456789ABCDEF0123456789ABCDEF", false},
		{"empty rejects", "", false},
		{"quoted rejects", `"0123456789abcdef0123456789abcdef"`, false},
		{"32-char with dash rejects", "0123456789abcdef-1234567890abcdef", false},
		{"32-char non-hex letter rejects", "0123456789abcdef0123456789abcdeg", false},
		{"32-char mixed case rejects", "0123456789ABCDEF0123456789abcdef", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContentMD5ETag(tt.etag); got != tt.want {
				t.Fatalf("ContentMD5ETag(%q) = %v, want %v", tt.etag, got, tt.want)
			}
		})
	}
}
