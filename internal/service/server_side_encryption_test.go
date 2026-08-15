package service

import "testing"

// TestServerSideEncryptionInfo pins the provider-managed SSE metadata read
// path that the thumbnail cache admission gate leans on (R2/AC-4): only
// AES256 and aws:kms report ok — absent/garbage metadata reports !ok, the
// KMS key ID round-trips on the aws:kms arm, and AES256 is deliberately
// admitted by the gate (its ETag stays the content MD5). The function had
// zero direct coverage before the admission-gate direction; the REST-level
// SSE-KMS subtest exercises it only indirectly.
func TestServerSideEncryptionInfo(t *testing.T) {
	tests := []struct {
		name          string
		meta          map[string]string
		wantAlgorithm string
		wantKeyID     string
		wantOK        bool
	}{
		{"AES256 reports ok without key ID", map[string]string{"_aero_sse_algorithm": "AES256"}, "AES256", "", true},
		{"aws:kms reports ok with key ID", map[string]string{"_aero_sse_algorithm": "aws:kms", "_aero_sse_kms_key_id": "alias/testing"}, "aws:kms", "alias/testing", true},
		{"aws:kms reports ok without key ID", map[string]string{"_aero_sse_algorithm": "aws:kms"}, "aws:kms", "", true},
		{"absent metadata reports not ok", map[string]string{}, "", "", false},
		{"garbage algorithm reports not ok", map[string]string{"_aero_sse_algorithm": "AES128"}, "", "", false},
		{"nil metadata reports not ok", nil, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			algo, keyID, ok := ServerSideEncryptionInfo(tt.meta)
			if ok != tt.wantOK || algo != tt.wantAlgorithm || keyID != tt.wantKeyID {
				t.Fatalf("ServerSideEncryptionInfo(%v) = (%q, %q, %v), want (%q, %q, %v)",
					tt.meta, algo, keyID, ok, tt.wantAlgorithm, tt.wantKeyID, tt.wantOK)
			}
		})
	}
}
