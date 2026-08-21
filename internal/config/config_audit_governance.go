package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	maxAuditGovernanceBindingsBytes = 1 << 20
	maxAuditGovernanceRevision      = uint64(1<<63 - 1)
)

type AuditGovernanceConfig struct {
	Enabled                   bool
	Drain                     bool
	BaseURL                   string
	TokenURL                  string
	BindingsFile              string
	HMACKey                   string
	HTTPTimeoutSeconds        int
	PollMilliseconds          int
	BatchSize                 int
	ClaimTTLSeconds           int
	InitialBackoffSeconds     int
	MaxBackoffSeconds         int
	MaxLagSeconds             int
	ReconcileBatchSize        int
	DeliveredRetentionSeconds int
	CleanupIntervalSeconds    int
	CleanupBatchSize          int
	Revision                  uint64
	Bindings                  []AuditGovernanceBinding
}

// BacklogAlertThresholdSeconds returns the age-arm threshold of the
// AuditGovernanceBacklogDegraded alert's age arm: maxLag×0.5, floored. The
// deployed rule now derives this from audit_governance_max_lag_seconds, while
// this accessor remains the config-side arithmetic contract used by tests and
// documentation. Value receiver; zero I/O.
func (c AuditGovernanceConfig) BacklogAlertThresholdSeconds() int {
	return c.MaxLagSeconds / 2
}

type AuditGovernanceBinding struct {
	TenantID        string `json:"tenant_id"`
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"`
	State           string `json:"state,omitempty"`
	ClientSecret    string `json:"-"`
}

type auditGovernanceBindingsFile struct {
	Revision uint64                   `json:"revision"`
	Bindings []AuditGovernanceBinding `json:"bindings"`
}

// auditGovernanceBoolForms lists every spelling strconv.ParseBool accepts —
// the only set, non-empty values AUDIT_GOVERNANCE_ENABLED and
// AUDIT_GOVERNANCE_DRAIN may carry. Anything else is a boot error.
const auditGovernanceBoolForms = "1, t, T, TRUE, true, True, 0, f, F, FALSE, false, False"

// getAuditGovernanceEnvBool is the strict boolean parse used by the audit
// governance loader. The generic getEnvBool helper silently falls back to
// the default on parse failure, which is a fail-open-for-capture footgun on
// the enable flag (AUDIT_GOVERNANCE_ENABLED=yes would boot with the relay
// silently off) and a silent no-op on the drain flag (the operator believes
// draining while the relay keeps running). Unset and empty values are
// neutralized to the default exactly like getEnvBool — the strictness adds
// an explicit error, it does not narrow the accepted set — so every
// previously legal configuration loads identically.
func getAuditGovernanceEnvBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: invalid boolean value %q (accepted: %s)", key, v, auditGovernanceBoolForms)
	}
	return parsed, nil
}

func loadAuditGovernanceConfig() (AuditGovernanceConfig, error) {
	enabled, err := getAuditGovernanceEnvBool("AUDIT_GOVERNANCE_ENABLED", false)
	if err != nil {
		return AuditGovernanceConfig{}, err
	}
	drain, err := getAuditGovernanceEnvBool("AUDIT_GOVERNANCE_DRAIN", false)
	if err != nil {
		return AuditGovernanceConfig{}, err
	}
	env := &typedEnv{}
	cfg := AuditGovernanceConfig{
		Enabled:                   enabled,
		Drain:                     drain,
		BaseURL:                   strings.TrimRight(getEnv("AUDIT_GOVERNANCE_BASE_URL", ""), "/"),
		TokenURL:                  getEnv("AUDIT_GOVERNANCE_TOKEN_URL", ""),
		BindingsFile:              getEnv("AUDIT_GOVERNANCE_BINDINGS_FILE", ""),
		HMACKey:                   getEnv("AUDIT_GOVERNANCE_HMAC_KEY", ""),
		HTTPTimeoutSeconds:        env.Int("AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS", 5),
		PollMilliseconds:          env.Int("AUDIT_GOVERNANCE_POLL_MILLISECONDS", 1000),
		BatchSize:                 env.Int("AUDIT_GOVERNANCE_BATCH_SIZE", 32),
		ClaimTTLSeconds:           env.Int("AUDIT_GOVERNANCE_CLAIM_TTL_SECONDS", 30),
		InitialBackoffSeconds:     env.Int("AUDIT_GOVERNANCE_INITIAL_BACKOFF_SECONDS", 1),
		MaxBackoffSeconds:         env.Int("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300),
		MaxLagSeconds:             env.Int("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900),
		ReconcileBatchSize:        env.Int("AUDIT_GOVERNANCE_RECONCILE_BATCH_SIZE", 100),
		DeliveredRetentionSeconds: env.Int("AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS", 604800),
		CleanupIntervalSeconds:    env.Int("AUDIT_GOVERNANCE_CLEANUP_INTERVAL_SECONDS", 3600),
		CleanupBatchSize:          env.Int("AUDIT_GOVERNANCE_CLEANUP_BATCH_SIZE", 100),
	}
	if err := env.Err(); err != nil {
		return AuditGovernanceConfig{}, err
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	document, err := readAuditGovernanceBindings(cfg.BindingsFile)
	if err != nil {
		return AuditGovernanceConfig{}, err
	}
	cfg.Revision, cfg.Bindings = document.Revision, document.Bindings
	if err := cfg.Validate(); err != nil {
		return AuditGovernanceConfig{}, err
	}
	return cfg, nil
}

func readAuditGovernanceBindings(filename string) (auditGovernanceBindingsFile, error) {
	if filename == "" {
		return auditGovernanceBindingsFile{}, errors.New("AUDIT_GOVERNANCE_BINDINGS_FILE is required when enabled")
	}
	before, err := os.Lstat(filename)
	if err != nil {
		return auditGovernanceBindingsFile{}, fmt.Errorf("lstat audit governance bindings: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o022 != 0 {
		return auditGovernanceBindingsFile{}, errors.New("audit governance bindings must be a non-writable regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return auditGovernanceBindingsFile{}, fmt.Errorf("open audit governance bindings: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return auditGovernanceBindingsFile{}, errors.New("audit governance bindings changed while opening")
	}
	return decodeAuditGovernanceBindings(file, after.Size())
}

func decodeAuditGovernanceBindings(
	reader io.Reader, size int64,
) (auditGovernanceBindingsFile, error) {
	if size < 0 || size > maxAuditGovernanceBindingsBytes {
		return auditGovernanceBindingsFile{}, errors.New("audit governance bindings exceed 1 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(reader, maxAuditGovernanceBindingsBytes+1))
	decoder.DisallowUnknownFields()
	var document auditGovernanceBindingsFile
	if err := decoder.Decode(&document); err != nil {
		return document, fmt.Errorf("decode audit governance bindings: %w", err)
	}
	if err := ensureAuditGovernanceJSONEOF(decoder); err != nil {
		return document, fmt.Errorf("decode audit governance bindings: %w", err)
	}
	bindings, err := resolveAuditGovernanceSecrets(document.Bindings)
	document.Bindings = bindings
	return document, err
}

func ensureAuditGovernanceJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func resolveAuditGovernanceSecrets(
	bindings []AuditGovernanceBinding,
) ([]AuditGovernanceBinding, error) {
	for index := range bindings {
		binding := &bindings[index]
		binding.TenantID = strings.TrimSpace(binding.TenantID)
		binding.ClientID = strings.TrimSpace(binding.ClientID)
		binding.ClientSecretEnv = strings.TrimSpace(binding.ClientSecretEnv)
		binding.State = strings.TrimSpace(binding.State)
		if binding.State == "" {
			binding.State = "active"
		}
		if !envNamePattern.MatchString(binding.ClientSecretEnv) ||
			!strings.HasPrefix(binding.ClientSecretEnv, "AUDIT_GOVERNANCE_CLIENT_SECRET_") {
			return nil, fmt.Errorf("audit governance binding %d has invalid client_secret_env", index)
		}
		secret, ok := os.LookupEnv(binding.ClientSecretEnv)
		if !ok || secret == "" {
			return nil, fmt.Errorf("audit governance binding %d secret environment is unset", index)
		}
		binding.ClientSecret = secret
	}
	return bindings, nil
}

func (c AuditGovernanceConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	// Fail-closed activation gate: an enabled relay with zero bound tenants
	// silently stops capturing audit facts, so the empty-manifest boot is
	// refused here — first placement (before URL/HMAC/revision checks) makes
	// the error deterministic regardless of other env. The drain flag is the
	// only escape: it permits an empty manifest to apply the documented
	// disable-flow drain, and errors if it finds a non-empty one (it must not
	// survive as a silent sticky no-op into a re-enable). Both guards mirror
	// the BillingConfig.Validate binding guard (config_billing.go).
	if c.Drain && len(c.Bindings) > 0 {
		return errors.New("AUDIT_GOVERNANCE_DRAIN requires an empty bindings manifest")
	}
	if !c.Drain && len(c.Bindings) == 0 {
		return errors.New("audit governance bindings must not be empty")
	}
	if err := validateAuditGovernanceURL(c.BaseURL); err != nil {
		return fmt.Errorf("AUDIT_GOVERNANCE_BASE_URL: %w", err)
	}
	if err := validateAuditGovernanceURL(c.TokenURL); err != nil {
		return fmt.Errorf("AUDIT_GOVERNANCE_TOKEN_URL: %w", err)
	}
	if c.Revision == 0 || c.Revision > maxAuditGovernanceRevision {
		return errors.New("audit governance desired bindings require a positive revision")
	}
	if len(c.HMACKey) < 32 || len(c.HMACKey) > 4096 {
		return errors.New("AUDIT_GOVERNANCE_HMAC_KEY must contain 32..4096 bytes")
	}
	if err := validateAuditGovernanceDurations(c); err != nil {
		return err
	}
	if err := validateAuditGovernanceBindings(c.Bindings); err != nil {
		return err
	}
	for _, binding := range c.Bindings {
		if binding.ClientSecret == c.HMACKey {
			return errors.New("audit governance HMAC and OAuth credentials must be distinct")
		}
	}
	return nil
}

func validateAuditGovernanceURL(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || !validAuditGovernanceEndpoint(endpoint) {
		return errors.New("absolute URL without credentials, query, or fragment is required")
	}
	if endpoint.Scheme == "https" {
		return nil
	}
	if endpoint.Scheme != "http" || !auditGovernanceLoopback(endpoint.Hostname()) {
		return errors.New("HTTPS or loopback HTTP is required")
	}
	return nil
}

func validAuditGovernanceEndpoint(endpoint *url.URL) bool {
	return endpoint != nil && endpoint.Host != "" && endpoint.User == nil &&
		endpoint.RawQuery == "" && endpoint.Fragment == ""
}

func auditGovernanceLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateAuditGovernanceDurations(c AuditGovernanceConfig) error {
	if !validAuditGovernanceWorker(c) || !validAuditGovernanceRetry(c) {
		return errors.New("audit governance timeout, lease, retry, lag, or batch setting is invalid")
	}
	if !boundedAuditGovernanceTiming(c) {
		return errors.New("audit governance timing exceeds its safe maximum")
	}
	return nil
}

func validAuditGovernanceWorker(c AuditGovernanceConfig) bool {
	return c.HTTPTimeoutSeconds > 0 && c.PollMilliseconds > 0 && c.BatchSize > 0 &&
		c.BatchSize <= 500 && c.ClaimTTLSeconds > 2*c.HTTPTimeoutSeconds &&
		c.ReconcileBatchSize >= 2 && c.ReconcileBatchSize <= 500 &&
		c.CleanupBatchSize > 0 && c.CleanupBatchSize <= 500
}

// validAuditGovernanceRetry validates the retry timing envelope. The
// cumulative transient-retry window IS MaxBackoffSeconds (D3 — a single
// "cap" covers both the per-attempt delay and the total retry span), so the
// window gets its own floor: 2s..86400s (the harness/CI minimum and the
// bounded-timing cap). Below 2s the window is unrepresentable at CI speed
// (the webdav harness shrinks exactly to 2s) and the clock-skew safe
// direction (negative/zero elapsed never terminal) loses its margin.
func validAuditGovernanceRetry(c AuditGovernanceConfig) bool {
	return c.InitialBackoffSeconds > 0 && c.MaxBackoffSeconds >= 2 &&
		c.MaxBackoffSeconds >= c.InitialBackoffSeconds &&
		c.MaxLagSeconds > c.ClaimTTLSeconds
}

func boundedAuditGovernanceTiming(c AuditGovernanceConfig) bool {
	retentionOK := c.DeliveredRetentionSeconds >= 3_600 &&
		c.DeliveredRetentionSeconds <= 31_536_000
	cleanupOK := c.CleanupIntervalSeconds >= 60 && c.CleanupIntervalSeconds <= 86_400 &&
		c.CleanupIntervalSeconds <= c.DeliveredRetentionSeconds
	return c.HTTPTimeoutSeconds <= 29 && c.PollMilliseconds <= 60_000 &&
		c.ClaimTTLSeconds <= 60 && c.MaxBackoffSeconds <= 86_400 &&
		c.MaxLagSeconds <= 604_800 && retentionOK && cleanupOK
}

func validateAuditGovernanceBindings(bindings []AuditGovernanceBinding) error {
	tenants := make(map[string]struct{}, len(bindings))
	clients := make(map[string]struct{}, len(bindings))
	secretEnvs := make(map[string]struct{}, len(bindings))
	secrets := make(map[string]struct{}, len(bindings))
	for index, binding := range bindings {
		if !validBindingValue(binding.TenantID) || !validBindingValue(binding.ClientID) ||
			!validAuditSecretEnv(binding.ClientSecretEnv) || binding.ClientSecret == "" ||
			!validAuditGovernanceState(binding.State) {
			return fmt.Errorf("audit governance binding %d is invalid", index)
		}
		if _, exists := tenants[binding.TenantID]; exists {
			return fmt.Errorf("audit governance binding %d duplicates a tenant", index)
		}
		if _, exists := clients[binding.ClientID]; exists {
			return fmt.Errorf("audit governance binding %d duplicates a client_id", index)
		}
		if _, exists := secretEnvs[binding.ClientSecretEnv]; exists {
			return fmt.Errorf("audit governance binding %d duplicates a secret environment", index)
		}
		if _, exists := secrets[binding.ClientSecret]; exists {
			return fmt.Errorf("duplicate audit governance client secret at binding %d", index)
		}
		tenants[binding.TenantID] = struct{}{}
		clients[binding.ClientID] = struct{}{}
		secretEnvs[binding.ClientSecretEnv] = struct{}{}
		secrets[binding.ClientSecret] = struct{}{}
	}
	return nil
}

func validAuditGovernanceState(state string) bool {
	return state == "active" || state == "draining"
}

func validAuditSecretEnv(value string) bool {
	return envNamePattern.MatchString(value) && strings.HasPrefix(value, "AUDIT_GOVERNANCE_CLIENT_SECRET_")
}

func validBindingValue(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char == 0x7f {
			return false
		}
	}
	return true
}
