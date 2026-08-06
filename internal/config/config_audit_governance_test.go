package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validAuditGovernanceConfig() AuditGovernanceConfig {
	return AuditGovernanceConfig{
		Enabled: true, BaseURL: "https://audit.example", TokenURL: "https://sso.example/token",
		HMACKey:            "audit-governance-hmac-key-32-bytes-minimum",
		HTTPTimeoutSeconds: 5, PollMilliseconds: 1000, BatchSize: 32, ClaimTTLSeconds: 30,
		InitialBackoffSeconds: 1, MaxBackoffSeconds: 300, MaxLagSeconds: 900,
		ReconcileBatchSize: 100, DeliveredRetentionSeconds: 604800,
		CleanupIntervalSeconds: 3600, CleanupBatchSize: 100, Revision: 1,
		Bindings: []AuditGovernanceBinding{{TenantID: "acme", ClientID: "vault-audit",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_ACME", State: "active",
			ClientSecret: "audit-secret"}},
	}
}

func TestAuditGovernanceConfigRequiresSecureURLsAndBoundedRuntime(t *testing.T) {
	cfg := validAuditGovernanceConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	cfg.BaseURL = "http://audit.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("non-loopback HTTP base URL was accepted")
	}
	cfg = validAuditGovernanceConfig()
	cfg.TokenURL = "http://127.0.0.1:8080/token"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("loopback token URL rejected: %v", err)
	}
	cfg.ClaimTTLSeconds = 2 * cfg.HTTPTimeoutSeconds
	if err := cfg.Validate(); err == nil {
		t.Fatal("claim TTL not covering publish plus acknowledgement was accepted")
	}
	cfg = validAuditGovernanceConfig()
	cfg.HTTPTimeoutSeconds = 30
	if err := cfg.Validate(); err == nil {
		t.Fatal("HTTP timeout beyond shutdown contract was accepted")
	}
}

func TestAuditGovernanceBindingsFileIsStrictAndResolvesDedicatedSecret(t *testing.T) {
	t.Setenv("AUDIT_GOVERNANCE_CLIENT_SECRET_ACME", "audit-secret")
	path := filepath.Join(t.TempDir(), "bindings.json")
	body := `{"revision":3,"bindings":[{"tenant_id":"acme","client_id":"vault-audit","client_secret_env":"AUDIT_GOVERNANCE_CLIENT_SECRET_ACME"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write binding: %v", err)
	}
	document, err := readAuditGovernanceBindings(path)
	if err != nil || document.Revision != 3 || document.Bindings[0].ClientSecret != "audit-secret" {
		t.Fatalf("document=%+v err=%v", document, err)
	}
	if document.Bindings[0].State != "active" {
		t.Fatalf("default binding state=%q", document.Bindings[0].State)
	}
	if err := os.Chmod(path, 0o622); err != nil {
		t.Fatalf("chmod binding: %v", err)
	}
	if _, err := readAuditGovernanceBindings(path); err == nil {
		t.Fatal("group-writable bindings file was accepted")
	}
}

func TestAuditGovernanceBindingsRejectSymlinkUnknownAndBillingSecretEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUDIT_GOVERNANCE_CLIENT_SECRET_ACME", "audit-secret")
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte(`{"revision":1,"unknown":true,"bindings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuditGovernanceBindings(link); err == nil {
		t.Fatal("symlink bindings file was accepted")
	}
	if _, err := readAuditGovernanceBindings(real); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field error=%v", err)
	}
	_, err := resolveAuditGovernanceSecrets([]AuditGovernanceBinding{{TenantID: "acme",
		ClientID: "vault-audit", ClientSecretEnv: "BILLING_CLIENT_SECRET_ACME"}})
	if err == nil {
		t.Fatal("billing secret environment was accepted for audit")
	}
}

func TestCommercialMachineCredentialsMustBeSeparate(t *testing.T) {
	audit := validAuditGovernanceConfig()
	billing := BillingConfig{Enabled: true, Bindings: []BillingBinding{{
		TenantID: "acme", ClientID: "vault-audit", ClientSecretEnv: "BILLING_SECRET", ClientSecret: "other"}}}
	if err := validateCommercialCredentialSeparation(billing, audit); err == nil {
		t.Fatal("shared client ID was accepted")
	}
	billing.Bindings[0].ClientID = "billing-client"
	if err := validateCommercialCredentialSeparation(billing, audit); err != nil {
		t.Fatalf("separate credentials rejected: %v", err)
	}
	audit.HMACKey = billing.Bindings[0].ClientSecret
	audit.Bindings = nil
	if err := validateCommercialCredentialSeparation(billing, audit); err == nil {
		t.Fatal("Billing client secret was accepted as the audit HMAC key")
	}
}

func TestAuditGovernanceBindingsRequireDistinctCredentials(t *testing.T) {
	cfg := validAuditGovernanceConfig()
	cfg.HMACKey = cfg.Bindings[0].ClientSecret
	if err := cfg.Validate(); err == nil {
		t.Fatal("OAuth client secret was accepted as the HMAC key")
	}
	cfg = validAuditGovernanceConfig()
	second := AuditGovernanceBinding{TenantID: "other", ClientID: "other-audit",
		ClientSecretEnv: cfg.Bindings[0].ClientSecretEnv, ClientSecret: "other-secret"}
	cfg.Bindings = append(cfg.Bindings, second)
	if err := cfg.Validate(); err == nil {
		t.Fatal("shared secret environment was accepted")
	}
	second.ClientSecretEnv = "AUDIT_GOVERNANCE_CLIENT_SECRET_OTHER"
	second.ClientSecret = cfg.Bindings[0].ClientSecret
	cfg.Bindings[1] = second
	if err := cfg.Validate(); err == nil {
		t.Fatal("shared client secret was accepted")
	}
	cfg = validAuditGovernanceConfig()
	cfg.Revision++
	cfg.Bindings = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit empty desired bindings rejected: %v", err)
	}
}
