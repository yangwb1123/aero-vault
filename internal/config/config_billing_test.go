package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBillingConfigUsesEnvironmentSecret(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "bindings.json")
	body := []byte(`{"bindings":[{"tenant_id":"acme","client_id":"vault-acme","client_secret_env":"TEST_BILLING_SECRET"}]}`)
	if err := os.WriteFile(filename, body, 0o600); err != nil {
		t.Fatalf("write bindings: %v", err)
	}
	t.Setenv("BILLING_ENABLED", "true")
	t.Setenv("BILLING_BASE_URL", "https://billing.example.test")
	t.Setenv("BILLING_TOKEN_URL", "https://id.example.test/token")
	t.Setenv("BILLING_BINDINGS_FILE", filename)
	t.Setenv("TEST_BILLING_SECRET", "secret-from-env")
	cfg, err := loadBillingConfig()
	if err != nil {
		t.Fatalf("load billing config: %v", err)
	}
	if len(cfg.Bindings) != 1 || cfg.Bindings[0].TenantID != "acme" ||
		cfg.Bindings[0].ClientSecret != "secret-from-env" {
		t.Fatalf("unexpected binding: %+v", cfg.Bindings)
	}
}

func TestLoadBillingConfigRejectsSecretInBindingFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "bindings.json")
	body := []byte(`{"bindings":[{"tenant_id":"acme","client_id":"vault-acme","client_secret_env":"TEST_BILLING_SECRET","client_secret":"leak"}]}`)
	if err := os.WriteFile(filename, body, 0o600); err != nil {
		t.Fatalf("write bindings: %v", err)
	}
	t.Setenv("BILLING_ENABLED", "true")
	t.Setenv("BILLING_BINDINGS_FILE", filename)
	if _, err := loadBillingConfig(); err == nil {
		t.Fatal("expected unknown client_secret field to be rejected")
	}
}

func TestValidateBillingURLAllowsOnlyLoopbackHTTP(t *testing.T) {
	if err := validateBillingURL("http://127.0.0.1:8080", true); err != nil {
		t.Fatalf("loopback development URL rejected: %v", err)
	}
	if err := validateBillingURL("http://billing.internal", true); err == nil {
		t.Fatal("non-loopback HTTP URL accepted")
	}
	if err := validateBillingURL("https://billing.example.test", false); err != nil {
		t.Fatalf("HTTPS URL rejected: %v", err)
	}
}
