package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const maxBillingBindingsBytes = 1 << 20

var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type BillingConfig struct {
	Enabled               bool
	BaseURL               string
	TokenURL              string
	BindingsFile          string
	AllowInsecureHTTP     bool
	HTTPTimeoutSeconds    int
	ProjectionIntervalSec int
	OutboxPollMillis      int
	OutboxBatchSize       int
	ClaimTTLSeconds       int
	OutboxMaxAttempts     int
	MaxLagSeconds         int
	Bindings              []BillingBinding
}

type BillingBinding struct {
	TenantID        string `json:"tenant_id"`
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"`
	ClientSecret    string `json:"-"`
}

type billingBindingsFile struct {
	Bindings []BillingBinding `json:"bindings"`
}

func loadBillingConfig() (BillingConfig, error) {
	env := &typedEnv{}
	cfg := BillingConfig{
		Enabled:               env.Bool("BILLING_ENABLED", false),
		BaseURL:               strings.TrimRight(getEnv("BILLING_BASE_URL", ""), "/"),
		TokenURL:              getEnv("BILLING_TOKEN_URL", ""),
		BindingsFile:          getEnv("BILLING_BINDINGS_FILE", ""),
		AllowInsecureHTTP:     env.Bool("BILLING_ALLOW_INSECURE_HTTP", false),
		HTTPTimeoutSeconds:    env.Int("BILLING_HTTP_TIMEOUT_SECONDS", 5),
		ProjectionIntervalSec: env.Int("BILLING_PROJECTION_INTERVAL_SECONDS", 60),
		OutboxPollMillis:      env.Int("BILLING_OUTBOX_POLL_MILLISECONDS", 1000),
		OutboxBatchSize:       env.Int("BILLING_OUTBOX_BATCH_SIZE", 32),
		ClaimTTLSeconds:       env.Int("BILLING_OUTBOX_CLAIM_TTL_SECONDS", 30),
		OutboxMaxAttempts:     env.Int("BILLING_OUTBOX_MAX_ATTEMPTS", 10),
		MaxLagSeconds:         env.Int("BILLING_MAX_LAG_SECONDS", 900),
	}
	if err := env.Err(); err != nil {
		return BillingConfig{}, err
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	bindings, err := readBillingBindings(cfg.BindingsFile)
	if err != nil {
		return BillingConfig{}, err
	}
	cfg.Bindings = bindings
	if err := cfg.Validate(); err != nil {
		return BillingConfig{}, err
	}
	return cfg, nil
}

func readBillingBindings(filename string) ([]BillingBinding, error) {
	if filename == "" {
		return nil, errors.New("BILLING_BINDINGS_FILE is required when billing is enabled")
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open billing bindings: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat billing bindings: %w", err)
	}
	if info.Size() > maxBillingBindingsBytes {
		return nil, errors.New("billing bindings file exceeds 1 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(f, maxBillingBindingsBytes))
	decoder.DisallowUnknownFields()
	var document billingBindingsFile
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode billing bindings: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return resolveBillingSecrets(document.Bindings)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode billing bindings: trailing JSON value")
		}
		return fmt.Errorf("decode billing bindings: %w", err)
	}
	return nil
}

func resolveBillingSecrets(bindings []BillingBinding) ([]BillingBinding, error) {
	for i := range bindings {
		bindings[i].TenantID = strings.TrimSpace(bindings[i].TenantID)
		bindings[i].ClientID = strings.TrimSpace(bindings[i].ClientID)
		bindings[i].ClientSecretEnv = strings.TrimSpace(bindings[i].ClientSecretEnv)
		if !envNamePattern.MatchString(bindings[i].ClientSecretEnv) {
			return nil, fmt.Errorf("billing binding %d has invalid client_secret_env", i)
		}
		secret, ok := os.LookupEnv(bindings[i].ClientSecretEnv)
		if !ok || secret == "" {
			return nil, fmt.Errorf("billing binding %d secret environment is unset", i)
		}
		bindings[i].ClientSecret = secret
	}
	return bindings, nil
}

func (c BillingConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if err := validateBillingURL(c.BaseURL, c.AllowInsecureHTTP); err != nil {
		return fmt.Errorf("BILLING_BASE_URL: %w", err)
	}
	if err := validateBillingURL(c.TokenURL, c.AllowInsecureHTTP); err != nil {
		return fmt.Errorf("BILLING_TOKEN_URL: %w", err)
	}
	if len(c.Bindings) == 0 {
		return errors.New("billing bindings must not be empty")
	}
	if c.HTTPTimeoutSeconds <= 0 || c.ProjectionIntervalSec <= 0 || c.OutboxPollMillis <= 0 ||
		c.OutboxBatchSize <= 0 || c.OutboxBatchSize > 500 || c.ClaimTTLSeconds <= c.HTTPTimeoutSeconds {
		return errors.New("billing timeout, intervals, batch size, or claim TTL is invalid")
	}
	if c.HTTPTimeoutSeconds > 120 || c.ProjectionIntervalSec > 86_400 ||
		c.OutboxPollMillis > 60_000 || c.ClaimTTLSeconds > 3_600 {
		return errors.New("billing timeout or interval exceeds its safe maximum")
	}
	if c.OutboxMaxAttempts <= 0 || c.OutboxMaxAttempts > 1000 {
		return errors.New("BILLING_OUTBOX_MAX_ATTEMPTS must be within 1..1000")
	}
	if c.MaxLagSeconds <= c.ClaimTTLSeconds || c.MaxLagSeconds > 604800 {
		return errors.New("BILLING_MAX_LAG_SECONDS must exceed BILLING_OUTBOX_CLAIM_TTL_SECONDS and be <= 604800")
	}
	return validateBillingBindings(c.Bindings)
}

func validateBillingURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("absolute URL without credentials, query, or fragment is required")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" || !allowInsecure {
		return errors.New("HTTPS is required")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("insecure HTTP is allowed only for loopback development")
	}
	return nil
}

func validateBillingBindings(bindings []BillingBinding) error {
	tenants := make(map[string]struct{}, len(bindings))
	clients := make(map[string]struct{}, len(bindings))
	for i, binding := range bindings {
		if binding.TenantID == "" || binding.ClientID == "" || binding.ClientSecret == "" {
			return fmt.Errorf("billing binding %d requires tenant_id, client_id, and secret", i)
		}
		if _, duplicate := tenants[binding.TenantID]; duplicate {
			return fmt.Errorf("duplicate billing tenant %q", binding.TenantID)
		}
		if _, duplicate := clients[binding.ClientID]; duplicate {
			return fmt.Errorf("duplicate billing client_id %q", binding.ClientID)
		}
		tenants[binding.TenantID] = struct{}{}
		clients[binding.ClientID] = struct{}{}
	}
	return nil
}
