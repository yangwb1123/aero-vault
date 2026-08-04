package config

import "testing"

func TestValidatePresignSecretLength(t *testing.T) {
	cfg := baseValid()
	cfg.Auth.PresignSecret = "too-short"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected short AUTH_PRESIGN_SECRET to be rejected")
	}
	cfg.Auth.PresignSecret = "0123456789abcdef0123456789abcdef"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("32-byte AUTH_PRESIGN_SECRET rejected: %v", err)
	}
}
