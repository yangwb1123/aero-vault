package config

import (
	"errors"
	"fmt"
	"time"
)

const maxOrphanGraceMinutes = int64(^uint64(0)>>1) / int64(time.Minute)

func (c *Config) Validate() error {
	if c.Reconcile.OrphanGraceMinutes < 0 {
		return errors.New("RECONCILE_ORPHAN_GRACE_MINUTES must be >= 0")
	}
	if int64(c.Reconcile.OrphanGraceMinutes) > maxOrphanGraceMinutes {
		return fmt.Errorf("RECONCILE_ORPHAN_GRACE_MINUTES must be <= %d (time.Duration-safe)", maxOrphanGraceMinutes)
	}
	if err := c.validateStorage(); err != nil {
		return err
	}
	switch c.DB.Driver {
	case "postgres", "sqlite":
	default:
		return fmt.Errorf("unknown DB_DRIVER %q", c.DB.Driver)
	}
	if c.DB.DSN == "" {
		return errors.New("DB_DSN is required")
	}
	if c.AI.Enabled && c.AI.Provider == "http" && c.AI.Endpoint == "" {
		return errors.New("AI_EMBED_ENDPOINT is required when AI_EMBED_PROVIDER=http")
	}
	if err := c.validateAI(); err != nil {
		return err
	}
	if err := c.validateTimeouts(); err != nil {
		return err
	}
	if c.Auth.PresignSecret != "" && len(c.Auth.PresignSecret) < 32 {
		return errors.New("AUTH_PRESIGN_SECRET must be at least 32 bytes")
	}
	if err := validateAuth(c.Auth); err != nil {
		return err
	}
	if err := validateAccess(c.Access, c.Auth); err != nil {
		return err
	}
	sink := c.effectiveAuditSink()
	if err := validateCommercialIntegrations(c.Billing, sink); err != nil {
		return err
	}
	c.EventOutbox = c.EventOutbox.withDefaults()
	if err := c.EventOutbox.Validate(); err != nil {
		return err
	}
	if err := sink.Validate(); err != nil {
		return err
	}
	return c.validateRateLimits()
}

func validateCommercialIntegrations(
	billing BillingConfig, sink AuditSinkConfig,
) error {
	if err := billing.Validate(); err != nil {
		return err
	}
	if err := sink.Legacy.Validate(); err != nil {
		return err
	}
	return validateCommercialCredentialSeparation(billing, sink)
}

// effectiveAuditSink keeps direct Config construction backwards compatible
// while making the selector the single validation/runtime surface. Load
// already fills AuditSink; older tests and embedders may still fill only the
// legacy fields on Config.
func (c *Config) effectiveAuditSink() AuditSinkConfig {
	sink := c.AuditSink
	if !sink.Legacy.Enabled && c.AuditGovernance.Enabled {
		sink.Legacy = c.AuditGovernance
	}
	if sink.Endpoint == "" && c.AuditSinkL2.Endpoint != "" {
		sink.Endpoint = c.AuditSinkL2.Endpoint
	}
	if sink.BindingsFile == "" && c.AuditSinkL2.BindingsFile != "" {
		sink.BindingsFile = c.AuditSinkL2.BindingsFile
	}
	if len(sink.Bindings) == 0 && len(c.AuditSinkL2.Bindings) > 0 {
		sink.Bindings = c.AuditSinkL2.Bindings
	}
	if sink.Kind == "" {
		switch {
		case sink.Legacy.Enabled, sink.Endpoint != "", sink.BindingsFile != "", len(sink.Bindings) > 0:
			sink.Kind = AuditSinkKindL2
		default:
			sink.Kind = AuditSinkKindL0
		}
	}
	return sink
}

// validateCommercialCredentialSeparation accepts the legacy type as well as
// AuditSinkConfig so existing package-local callers retain their old seam
// while the configuration validator covers bearer L2 credentials too.
func validateCommercialCredentialSeparation(billing BillingConfig, credentials any) error {
	switch value := credentials.(type) {
	case AuditGovernanceConfig:
		return validateLegacyCredentialSeparation(billing, value)
	case AuditSinkConfig:
		if err := validateLegacyCredentialSeparation(billing, value.Legacy); err != nil {
			return err
		}
		if !billing.Enabled || normalizeAuditSinkKind(value.Kind) != AuditSinkKindL2 || value.Legacy.Enabled {
			return nil
		}
		for _, auditBinding := range value.Bindings {
			for _, billingBinding := range billing.Bindings {
				if auditBinding.Token == billingBinding.ClientSecret {
					return errors.New("audit sink bearer token and billing credentials must be distinct")
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported audit credential configuration %T", credentials)
	}
}

func validateLegacyCredentialSeparation(
	billing BillingConfig, governance AuditGovernanceConfig,
) error {
	if !billing.Enabled || !governance.Enabled {
		return nil
	}
	for _, billingBinding := range billing.Bindings {
		if governance.HMACKey == billingBinding.ClientSecret {
			return errors.New("audit governance HMAC and billing credentials must be distinct")
		}
	}
	for _, auditBinding := range governance.Bindings {
		for _, billingBinding := range billing.Bindings {
			if auditBinding.ClientID == billingBinding.ClientID ||
				auditBinding.ClientSecretEnv == billingBinding.ClientSecretEnv ||
				auditBinding.ClientSecret == billingBinding.ClientSecret {
				return errors.New("billing and audit governance machine credentials must be distinct")
			}
		}
	}
	return nil
}

func validateAccess(cfg AccessConfig, auth AuthConfig) error {
	if cfg.DefaultPolicy != "" && cfg.DefaultPolicy != "deny" && cfg.DefaultPolicy != "tenant" {
		return errors.New("ACCESS_DEFAULT_POLICY must be deny or tenant")
	}
	if !cfg.Enabled {
		return nil
	}
	if len(cfg.ShareSecret) < 32 {
		return errors.New("ACCESS_SHARE_SECRET must be at least 32 bytes when access control is enabled")
	}
	if auth.Keys == "" && auth.JWTSecret == "" && auth.JWKSEndpoint == "" &&
		auth.SigV4Credentials == "" && !auth.PersistKeys {
		return errors.New("access control requires AUTH_KEYS, JWT/JWKS, SigV4, or persistent API keys")
	}
	return nil
}

func (c *Config) validateAI() error {
	if c.AI.ChatCostPromptPer1K < 0 || c.AI.ChatCostCompletionPer1K < 0 {
		return errors.New("AI cost rates must not be negative")
	}
	if c.AI.TenantDailyBudgetUSD < 0 {
		return errors.New("AI_TENANT_DAILY_BUDGET_USD must not be negative")
	}
	if c.AI.AgentMaxSteps < 0 {
		return errors.New("AI_AGENT_MAX_STEPS must not be negative")
	}
	if c.AI.SearchCacheSize < 0 || c.AI.SearchCacheTTLSeconds < 0 {
		return errors.New("AI search cache values must not be negative")
	}
	if c.AI.SearchCacheSize > 0 && c.AI.SearchCacheTTLSeconds == 0 {
		return errors.New("AI_SEARCH_CACHE_TTL_SECONDS must be positive when cache is enabled")
	}
	return nil
}

func (c *Config) validateStorage() error {
	switch c.Storage.Backend {
	case "local":
		if c.Storage.Local.Root == "" {
			return errors.New("STORAGE_LOCAL_ROOT is required for local backend")
		}
	case "s3":
		if c.Storage.S3.Bucket == "" {
			return errors.New("STORAGE_S3_BUCKET is required for s3 backend")
		}
	case "oss":
		if c.Storage.OSS.Endpoint == "" || c.Storage.OSS.Bucket == "" {
			return errors.New("STORAGE_OSS_ENDPOINT and STORAGE_OSS_BUCKET are required for oss backend")
		}
	case "cos":
		if c.Storage.COS.BucketURL == "" {
			return errors.New("STORAGE_COS_BUCKET_URL is required for cos backend")
		}
	default:
		return fmt.Errorf("unknown STORAGE_BACKEND %q", c.Storage.Backend)
	}
	return nil
}

func (c *Config) validateTimeouts() error {
	if c.App.WriteTimeoutSec < 0 {
		return errors.New("APP_WRITE_TIMEOUT must be >= 0 (0 = disabled)")
	}
	if c.App.IdleTimeoutSec < 0 {
		return errors.New("APP_IDLE_TIMEOUT must be >= 0 (0 = disabled)")
	}
	if c.App.TLSEnabled && (c.App.TLSCertFile == "" || c.App.TLSKeyFile == "") {
		return errors.New("APP_TLS_CERT_FILE and APP_TLS_KEY_FILE are required when APP_TLS_ENABLED=true")
	}
	if c.App.RequestTimeoutSec < 0 {
		return errors.New("REQUEST_TIMEOUT_SECONDS must be >= 0 (0 = disabled)")
	}
	if c.App.ThumbnailCacheBytes < 0 {
		return errors.New("THUMBNAIL_CACHE_BYTES must be >= 0 (0 = disabled)")
	}
	if c.App.ThumbnailCacheTTL < 0 {
		return errors.New("THUMBNAIL_CACHE_TTL must be >= 0 (0 = disabled)")
	}
	if c.App.ThumbnailPerTenantDecodeSlots < 0 {
		return errors.New("THUMBNAIL_PER_TENANT_DECODE_SLOTS must be >= 0 (0 = disabled)")
	}
	// Upper bound: values beyond one year would overflow time.Duration's
	// nanosecond range at the wiring site (cmd/server/http.go), silently
	// wrapping negative and disabling expiry — fail fast instead.
	if c.App.ThumbnailCacheTTL > 31536000 {
		return errors.New("THUMBNAIL_CACHE_TTL must be <= 31536000 (1 year)")
	}
	return nil
}

func (c *Config) validateRateLimits() error {
	if (c.RateLimit.RPS > 0) != (c.RateLimit.Burst > 0) {
		return errors.New("RATE_LIMIT_RPS and RATE_LIMIT_BURST must both be positive or both be zero")
	}
	if (c.RateLimit.AIRPS > 0) != (c.RateLimit.AIBurst > 0) {
		return errors.New("AI_RATE_LIMIT_RPS and AI_RATE_LIMIT_BURST must both be positive or both be zero")
	}
	if (c.RateLimit.AdminRPS > 0) != (c.RateLimit.AdminBurst > 0) {
		return errors.New("ADMIN_RATE_LIMIT_RPS and ADMIN_RATE_LIMIT_BURST must both be positive or both be zero")
	}
	if c.RateLimit.RPS < 0 || c.RateLimit.Burst < 0 || c.RateLimit.AIRPS < 0 || c.RateLimit.AIBurst < 0 || c.RateLimit.AdminRPS < 0 || c.RateLimit.AdminBurst < 0 {
		return errors.New("rate limit values must not be negative")
	}
	return nil
}
