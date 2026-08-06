package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// AuditSinkL2Config configures the L2 egress of the AuditSink port (FR-2).
// Empty Endpoint disables L2 entirely — the relay then completes deleted@1.1
// facts without delivery and L0 audit_log stays authoritative (C3).
type AuditSinkL2Config struct {
	Endpoint     string // AUDIT_SINK_L2_ENDPOINT; empty → L2 disabled
	BindingsFile string // AUDIT_SINK_L2_BINDINGS_FILE; JSON {"bindings":[{"tenant","token"|"token_env"}]}
	Bindings     []AuditSinkL2Binding
}

// AuditSinkL2Binding is one tenant → static bearer token mapping. Exactly one
// of Token (plaintext in the file — requires mode 0600, H4) or TokenEnv (env
// indirection, recommended) must resolve.
type AuditSinkL2Binding struct {
	Tenant   string `json:"tenant"`
	Token    string `json:"token,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

type auditSinkL2BindingsFile struct {
	Bindings []AuditSinkL2Binding `json:"bindings"`
}

// loadAuditSinkL2Config reads the env keys and, when a bindings file is
// configured, parses it eagerly (fail-fast on corruption, F6). Validation of
// the endpoint scheme happens in Validate (H1).
func loadAuditSinkL2Config() (AuditSinkL2Config, error) {
	cfg := AuditSinkL2Config{
		Endpoint:     strings.TrimSpace(getEnv("AUDIT_SINK_L2_ENDPOINT", "")),
		BindingsFile: getEnv("AUDIT_SINK_L2_BINDINGS_FILE", ""),
	}
	if cfg.BindingsFile == "" {
		return cfg, nil
	}
	document, err := readAuditSinkL2Bindings(cfg.BindingsFile)
	if err != nil {
		return AuditSinkL2Config{}, err
	}
	cfg.Bindings = document.Bindings
	if err := validateAuditSinkL2Bindings(cfg.Bindings); err != nil {
		return AuditSinkL2Config{}, err
	}
	return cfg, nil
}

// readAuditSinkL2Bindings mirrors the governance bindings discipline (regular
// file, lstat/open same-file, ≤1 MiB, DisallowUnknownFields, trailing-JSON
// rejection) and tightens the permission rule: the file holds plaintext
// bearer tokens, so group/other read access is rejected (mode&077==0; the
// governance 0o022 rule only blocks writes — insufficient for secrets, H4).
func readAuditSinkL2Bindings(filename string) (auditSinkL2BindingsFile, error) {
	if filename == "" {
		return auditSinkL2BindingsFile{}, errors.New("AUDIT_SINK_L2_BINDINGS_FILE is required when configured")
	}
	before, err := os.Lstat(filename)
	if err != nil {
		return auditSinkL2BindingsFile{}, fmt.Errorf("lstat audit sink L2 bindings: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return auditSinkL2BindingsFile{}, errors.New("audit sink L2 bindings must be a regular file readable only by its owner (mode 0600)")
	}
	file, err := os.Open(filename)
	if err != nil {
		return auditSinkL2BindingsFile{}, fmt.Errorf("open audit sink L2 bindings: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return auditSinkL2BindingsFile{}, errors.New("audit sink L2 bindings changed while opening")
	}
	return decodeAuditSinkL2Bindings(file, after.Size())
}

func decodeAuditSinkL2Bindings(reader io.Reader, size int64) (auditSinkL2BindingsFile, error) {
	if size < 0 || size > maxAuditGovernanceBindingsBytes {
		return auditSinkL2BindingsFile{}, errors.New("audit sink L2 bindings exceed 1 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(reader, maxAuditGovernanceBindingsBytes+1))
	decoder.DisallowUnknownFields()
	var document auditSinkL2BindingsFile
	if err := decoder.Decode(&document); err != nil {
		// Decode errors name the offending field, never the token value (H5).
		return document, fmt.Errorf("decode audit sink L2 bindings: %w", err)
	}
	if err := ensureAuditGovernanceJSONEOF(decoder); err != nil {
		return document, fmt.Errorf("decode audit sink L2 bindings: %w", err)
	}
	return resolveAuditSinkL2Tokens(document), nil
}

// resolveAuditSinkL2Tokens resolves each binding's token: the inline Token
// field or the TokenEnv indirection (preferred — keeps the plaintext out of
// the filesystem, H4). Tokens are NOT normalized here: a token that differs
// from its trimmed form is rejected by validation (whitespace padding is a
// paste error, H2). The resolved Token is never logged or serialized.
func resolveAuditSinkL2Tokens(document auditSinkL2BindingsFile) auditSinkL2BindingsFile {
	for index := range document.Bindings {
		binding := &document.Bindings[index]
		binding.Tenant = strings.TrimSpace(binding.Tenant)
		binding.TokenEnv = strings.TrimSpace(binding.TokenEnv)
		if binding.TokenEnv != "" {
			if token, ok := os.LookupEnv(binding.TokenEnv); ok {
				binding.Token = token
			}
		}
	}
	return document
}

// validateAuditSinkL2Bindings enforces the token hygiene rules (H2/H4):
// non-empty, no leading/trailing whitespace, ≥16 chars, no duplicate tenants
// or tokens; a token_env reference must name an env var that resolves.
func validateAuditSinkL2Bindings(bindings []AuditSinkL2Binding) error {
	tenants := make(map[string]struct{}, len(bindings))
	tokens := make(map[string]struct{}, len(bindings))
	for index, binding := range bindings {
		if binding.Tenant == "" || binding.Tenant != strings.TrimSpace(binding.Tenant) {
			return fmt.Errorf("audit sink L2 binding %d has an invalid tenant", index)
		}
		if binding.TokenEnv != "" &&
			(!envNamePattern.MatchString(binding.TokenEnv) ||
				!strings.HasPrefix(binding.TokenEnv, "AUDIT_SINK_L2_TOKEN_")) {
			return fmt.Errorf("audit sink L2 binding %d has an invalid token_env", index)
		}
		if binding.TokenEnv != "" && binding.Token == "" {
			return fmt.Errorf("audit sink L2 binding %d token environment is unset", index)
		}
		if len(binding.Token) < 16 {
			return fmt.Errorf("audit sink L2 binding %d token must be at least 16 characters", index)
		}
		if binding.Token != strings.TrimSpace(binding.Token) {
			return fmt.Errorf("audit sink L2 binding %d token must not have surrounding whitespace", index)
		}
		if _, exists := tenants[binding.Tenant]; exists {
			return fmt.Errorf("audit sink L2 binding %d duplicates a tenant", index)
		}
		if _, exists := tokens[binding.Token]; exists {
			return fmt.Errorf("duplicate audit sink L2 token at binding %d", index)
		}
		tenants[binding.Tenant] = struct{}{}
		tokens[binding.Token] = struct{}{}
	}
	return nil
}

// Validate enforces the H1 endpoint rule. An empty endpoint means L2 is
// disabled and is always valid; bindings were already validated at load.
func (c AuditSinkL2Config) Validate() error {
	if c.Endpoint == "" {
		return nil
	}
	if err := validateAuditGovernanceURL(c.Endpoint); err != nil {
		return fmt.Errorf("AUDIT_SINK_L2_ENDPOINT: %w", err)
	}
	return validateAuditSinkL2Bindings(c.Bindings)
}
