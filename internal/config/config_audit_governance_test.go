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

func TestAuditGovernanceCumulativeWindowEnvelope(t *testing.T) {
	// D3: the cumulative transient-retry window IS MaxBackoffSeconds — a
	// single "cap 300s" covers both the per-attempt delay and the total retry
	// span. Its validation envelope is 2s..86400s: 2s is the harness/CI
	// minimum (the webdav e2e shrinks exactly to 2s), 86400s the bounded
	// timing cap. Below the floor the strict-> window decision loses its
	// margin against clock jitter; above the cap the operator intent is
	// indistinguishable from unbounded retry.
	cfg := validAuditGovernanceConfig()
	cfg.MaxBackoffSeconds = 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("cumulative retry window below the 2s floor was accepted")
	}
	for _, bound := range []int{2, 86400} {
		cfg = validAuditGovernanceConfig()
		cfg.MaxBackoffSeconds = bound
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%ds cumulative retry window rejected: %v", bound, err)
		}
	}
	cfg = validAuditGovernanceConfig()
	cfg.MaxBackoffSeconds = 86401
	if err := cfg.Validate(); err == nil {
		t.Fatal("cumulative retry window beyond the 86400s cap was accepted")
	}
}

func TestAuditGovernanceMaxBackoffDefaultIsCumulativeWindow(t *testing.T) {
	// AC-3.4: the production default window is 300s — pinned by reading the
	// same default the relay consumes, with no wall-clock wait.
	t.Setenv("AUDIT_GOVERNANCE_ENABLED", "false")
	cfg, err := loadAuditGovernanceConfig()
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	if cfg.MaxBackoffSeconds != 300 {
		t.Fatalf("default MaxBackoffSeconds=%d, want 300", cfg.MaxBackoffSeconds)
	}
}

func TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold(t *testing.T) {
	// E7/B3-2: the production default max lag is 900s. The alerts.yml
	// AuditGovernanceBacklogDegraded expr threshold (450s = maxLag×0.5) is
	// mechanically DERIVED from this same default in cmd/server
	// (TestAlertsYMLAuditGovernanceExprParity — config.Load()/2, const-free),
	// so this pin anchors the default side of the half-lag arithmetic: a
	// unilateral 900→N default drift now fails either this test or the
	// alerts.yml parity, never silently. Read through the same loader the
	// relay consumes, with no wall-clock wait (mirror of the backoff test).
	// The MaxLag env is neutralized (unlike the backoff mirror) because this
	// pin anchors the static alerts.yml arithmetic: it must read the SHIPPED
	// default even under an ambient AUDIT_GOVERNANCE_MAX_LAG_SECONDS override.
	t.Setenv("AUDIT_GOVERNANCE_ENABLED", "false")
	t.Setenv("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", "") // empty → getEnvInt falls back to the shipped default
	cfg, err := loadAuditGovernanceConfig()
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	if cfg.MaxLagSeconds != 900 {
		t.Fatalf("default MaxLagSeconds=%d, want 900", cfg.MaxLagSeconds)
	}
}

func TestAuditGovernanceBacklogAlertThresholdDerived(t *testing.T) {
	// REQ-2: BacklogAlertThresholdSeconds is the SINGLE ×0.5 derivation site
	// for the AuditGovernanceBacklogDegraded alert's age arm (450s = the
	// shipped 900s default × 0.5). Every want below is written as derived
	// arithmetic (900/2, 1800/2, 901/2) — never a literal 450 — because
	// TestNoExecutable450LiteralOutsideAlertsYml (cmd/server) strips
	// comments and string literals and bans executable 450 tokens outside
	// alerts.yml (FM5/F1/H1): the ban forces derivation at test level too,
	// reinforcing the single-site invariant. 901/2 == 450 (Go floor
	// division) pins the floor semantics with no banned token — a ceil
	// (n+1)/2 implementation would yield 451 and fail (FM2). Floor is the
	// safe direction for a warning alert: 450 < 901, so the age arm still
	// fires before the Ready() warn.
	t.Run("shipped default via loader", func(t *testing.T) {
		// Loader path, env-neutralized (mirror of the default pin): the
		// SHIPPED default must be read even under an ambient override. A
		// default drift 900→N fails here via the accessor, forcing the
		// alerts.yml parity update (FM5/FM6).
		t.Setenv("AUDIT_GOVERNANCE_ENABLED", "false")
		t.Setenv("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", "") // empty → getEnvInt falls back
		cfg, err := loadAuditGovernanceConfig()
		if err != nil {
			t.Fatalf("load default config: %v", err)
		}
		if got := cfg.BacklogAlertThresholdSeconds(); got != 900/2 {
			t.Fatalf("default BacklogAlertThresholdSeconds()=%d, want %d", got, 900/2)
		}
	})
	for _, maxLag := range []int{900, 1800, 901, 4} {
		// Struct form: the 1800 case fails a hardcoded (field-ignoring)
		// accessor; the 901 case distinguishes floor from ceil. The
		// threshold < maxLag ordering holds for every VALID config via the
		// validation chain (ClaimTTLSeconds > 2×HTTPTimeoutSeconds ≥ 2 ∧
		// MaxLagSeconds > ClaimTTLSeconds → MaxLagSeconds ≥ 4 →
		// MaxLagSeconds/2 < MaxLagSeconds) — the alert age arm always
		// fires before the Ready() warn (REQ-4 ordering, F3 hardening).
		cfg := validAuditGovernanceConfig()
		cfg.MaxLagSeconds = maxLag
		if got, want := cfg.BacklogAlertThresholdSeconds(), maxLag/2; got != want {
			t.Fatalf("MaxLagSeconds=%d: BacklogAlertThresholdSeconds()=%d, want %d", maxLag, got, want)
		}
		if got := cfg.BacklogAlertThresholdSeconds(); got >= cfg.MaxLagSeconds {
			t.Fatalf("MaxLagSeconds=%d: threshold %d must precede the Ready() warn at maxLag", maxLag, got)
		}
	}
	// Zero value: no panic, deterministic, misuse-bounded — the zero value
	// is not a valid config (fails Validate), and the sole production
	// consumer is the parity test via validated config.Load() (FM7).
	if got := (AuditGovernanceConfig{}).BacklogAlertThresholdSeconds(); got != 0 {
		t.Fatalf("zero-value BacklogAlertThresholdSeconds()=%d, want 0", got)
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
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "bindings") {
		t.Fatalf("empty desired bindings (nil) accepted, err=%v", err)
	}
	// AC-4.1 [] form: the guard must cover both nil and empty-slice manifests.
	cfg = validAuditGovernanceConfig()
	cfg.Revision++
	cfg.Bindings = []AuditGovernanceBinding{}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "bindings") {
		t.Fatalf("empty desired bindings ([]) accepted, err=%v", err)
	}
}

// TestAuditGovernanceDrainFlagRequiresEmptyManifest pins rule 1: the drain
// flag must fail closed — AUDIT_GOVERNANCE_DRAIN=true with a non-empty
// manifest is a hard boot error (first-placement-deterministic, so it fires
// before any URL/HMAC/duration check and needs no other env). A silently
// ignored flag would survive into a re-enable and permanently disarm the
// empty-bindings gate.
func TestAuditGovernanceDrainFlagRequiresEmptyManifest(t *testing.T) {
	cfg := validAuditGovernanceConfig()
	cfg.Drain = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "AUDIT_GOVERNANCE_DRAIN") {
		t.Fatalf("drain flag with non-empty manifest accepted, err=%v", err)
	}
	// Drain + empty manifest is the legal escape: both len-guards pass.
	cfg = validAuditGovernanceConfig()
	cfg.Revision++
	cfg.Bindings = nil
	cfg.Drain = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("drain flag with empty manifest rejected: %v", err)
	}
}

// TestAuditGovernanceEmptyBindingsLoadPathFailsClosed pins AC-4.1d: the
// boot path (loadAuditGovernanceConfig → Validate) refuses an enabled
// empty-manifest load with the bindings error. First placement makes the
// pin deterministic with only ENABLED + BINDINGS_FILE set — unset URL/HMAC
// env would otherwise mask it. The empty manifest skips
// resolveAuditGovernanceSecrets (zero bindings), and unset DRAIN defaults
// to false so the gate fires.
func TestAuditGovernanceEmptyBindingsLoadPathFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{"revision":1,"bindings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUDIT_GOVERNANCE_ENABLED", "true")
	t.Setenv("AUDIT_GOVERNANCE_BINDINGS_FILE", path)
	t.Setenv("AUDIT_GOVERNANCE_DRAIN", "") // empty → getEnvBool default false → gate fires
	if _, err := loadAuditGovernanceConfig(); err == nil || !strings.Contains(err.Error(), "bindings") {
		t.Fatalf("enabled empty-manifest load accepted, err=%v", err)
	}
}

// TestAuditGovernanceBooleanFlagsFailClosedOnNonCanonicalValues pins the
// security hardening for the silent-coercion footgun in getEnvBool: a set,
// non-empty, non-canonical boolean (the common "yes"/"on" spellings) would
// silently parse to the default — for AUDIT_GOVERNANCE_ENABLED that is
// capture silently off (fail-open for the audit trail), for
// AUDIT_GOVERNANCE_DRAIN a silent no-op (the operator believes the drain
// ran). The audit governance loader parses both flags strictly
// (getAuditGovernanceEnvBool) and refuses the load — GATE #1/#2 at
// config-load time, before any repo/store I/O — with an error naming the
// flag and the offending value.
func TestAuditGovernanceBooleanFlagsFailClosedOnNonCanonicalValues(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"enabled yes", "AUDIT_GOVERNANCE_ENABLED", "yes"},
		{"enabled on", "AUDIT_GOVERNANCE_ENABLED", "on"},
		{"enabled 2", "AUDIT_GOVERNANCE_ENABLED", "2"},
		{"drain yes", "AUDIT_GOVERNANCE_DRAIN", "yes"},
		{"drain tru", "AUDIT_GOVERNANCE_DRAIN", "tru"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(c.key, c.val)
			_, err := loadAuditGovernanceConfig()
			if err == nil || !strings.Contains(err.Error(), c.key) || !strings.Contains(err.Error(), c.val) {
				t.Fatalf("%s=%q: err=%v, want error naming both flag and value", c.key, c.val, err)
			}
		})
	}
}

// TestAuditGovernanceEnvBoolCanonicalAndNeutralized pins the strict parse
// at helper level: every strconv.ParseBool spelling still parses, and
// unset/empty still neutralize to the default. The strictness adds an
// explicit error on non-canonical values; it does not narrow the accepted
// set — which is what keeps the B3-6 gate semantics and the drain escape
// matrix identical for every previously legal configuration.
func TestAuditGovernanceEnvBoolCanonicalAndNeutralized(t *testing.T) {
	for _, canonical := range []struct {
		val  string
		want bool
	}{
		{"true", true}, {"TRUE", true}, {"True", true}, {"t", true}, {"T", true}, {"1", true},
		{"false", false}, {"FALSE", false}, {"False", false}, {"f", false}, {"F", false}, {"0", false},
	} {
		t.Setenv("AERO_TEST_AG_BOOL", canonical.val)
		got, err := getAuditGovernanceEnvBool("AERO_TEST_AG_BOOL", false)
		if err != nil || got != canonical.want {
			t.Fatalf("getAuditGovernanceEnvBool(%q) = %v, %v; want %v, nil", canonical.val, got, err, canonical.want)
		}
	}
	t.Run("unset neutralizes to default", func(t *testing.T) {
		got, err := getAuditGovernanceEnvBool("AERO_TEST_AG_UNSET", true)
		if err != nil || !got {
			t.Fatalf("unset = %v, %v; want true, nil", got, err)
		}
	})
	t.Run("empty neutralizes to default", func(t *testing.T) {
		t.Setenv("AERO_TEST_AG_EMPTY", "")
		got, err := getAuditGovernanceEnvBool("AERO_TEST_AG_EMPTY", false)
		if err != nil || got {
			t.Fatalf("empty = %v, %v; want false, nil", got, err)
		}
	})
}
