package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	App             AppConfig
	Storage         StorageConfig
	DB              DBConfig
	S3Compat        S3CompatConfig
	Events          EventsConfig
	AI              AIConfig
	Auth            AuthConfig
	Access          AccessConfig
	CORS            CORSCfg
	RateLimit       RateLimitCfg
	Reconcile       ReconcileCfg
	Jobs            JobsCfg
	Antivirus       AntivirusCfg
	Replication     ReplicationCfg
	WebDAV          WebDAVCfg
	WebUI           WebUICfg
	Telemetry       TelemetryCfg
	Billing         BillingConfig
	AuditGovernance AuditGovernanceConfig
	EventOutbox     EventOutboxConfig
	AuditSinkL2     AuditSinkL2Config
	AuditSink       AuditSinkConfig
}

type AppConfig struct {
	Addr                          string
	LogLevel                      slog.Level
	TLSEnabled                    bool   // APP_TLS_ENABLED
	TLSCertFile                   string // APP_TLS_CERT_FILE
	TLSKeyFile                    string // APP_TLS_KEY_FILE
	WriteTimeoutSec               int    // APP_WRITE_TIMEOUT; 0 = disabled
	IdleTimeoutSec                int    // APP_IDLE_TIMEOUT; 0 = disabled
	RequestTimeoutSec             int    // REQUEST_TIMEOUT_SECONDS; 0 = disabled
	MaxInFlight                   int    // MAX_INFLIGHT_REQUESTS; 0 = unlimited
	PerTenantMax                  int    // PER_TENANT_CONCURRENCY_MAX; 0 = unlimited
	MaxBodySize                   int    // APP_MAX_BODY_SIZE; max request body bytes (0 = unlimited)
	ThumbnailCacheBytes           int64  // THUMBNAIL_CACHE_BYTES; server-side thumbnail output cache (bytes); 0 = disabled
	ThumbnailCacheTTL             int    // THUMBNAIL_CACHE_TTL; seconds; 0 = disabled (unbounded)
	ThumbnailPerTenantDecodeSlots int    // THUMBNAIL_PER_TENANT_DECODE_SLOTS; 0 = disabled
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	logLevel, err := parseLogLevel(getEnv("APP_LOG_LEVEL", "info"))
	if err != nil {
		return nil, err
	}
	billing, err := loadBillingConfig()
	if err != nil {
		return nil, err
	}
	auditGovernance, err := loadAuditGovernanceConfig()
	if err != nil {
		return nil, err
	}
	auditSinkL2, err := loadAuditSinkL2Config()
	if err != nil {
		return nil, err
	}
	auditSink, err := loadAuditSinkConfig(auditGovernance, auditSinkL2)
	if err != nil {
		return nil, err
	}
	eventOutbox, err := loadEventOutboxConfig()
	if err != nil {
		return nil, err
	}
	env := &typedEnv{}

	cfg := &Config{
		App: AppConfig{
			Addr:                          getEnv("APP_ADDR", ":8080"),
			LogLevel:                      logLevel,
			TLSEnabled:                    env.Bool("APP_TLS_ENABLED", false),
			TLSCertFile:                   getEnv("APP_TLS_CERT_FILE", ""),
			TLSKeyFile:                    getEnv("APP_TLS_KEY_FILE", ""),
			WriteTimeoutSec:               env.Int("APP_WRITE_TIMEOUT", 60),
			IdleTimeoutSec:                env.Int("APP_IDLE_TIMEOUT", 120),
			RequestTimeoutSec:             env.Int("REQUEST_TIMEOUT_SECONDS", 120),
			MaxInFlight:                   env.Int("MAX_INFLIGHT_REQUESTS", 0),
			PerTenantMax:                  env.Int("PER_TENANT_CONCURRENCY_MAX", 0),
			MaxBodySize:                   env.Int("APP_MAX_BODY_SIZE", 0),
			ThumbnailCacheBytes:           int64(env.Int("THUMBNAIL_CACHE_BYTES", 0)),
			ThumbnailCacheTTL:             env.Int("THUMBNAIL_CACHE_TTL", 0),
			ThumbnailPerTenantDecodeSlots: env.Int("THUMBNAIL_PER_TENANT_DECODE_SLOTS", 0),
		},
		Storage: StorageConfig{
			Backend:            strings.ToLower(getEnv("STORAGE_BACKEND", "local")),
			SSERewrapOnStart:   env.Bool("STORAGE_SSE_REWRAP_ON_START", false),
			DefaultClass:       getEnv("STORAGE_DEFAULT_CLASS", ""),
			ConnectTimeout:     env.Int("STORAGE_CONNECT_TIMEOUT", 5),
			ReadTimeout:        env.Int("STORAGE_READ_TIMEOUT", 30),
			WriteTimeout:       env.Int("STORAGE_WRITE_TIMEOUT", 30),
			VerifyOnRead:       env.Bool("STORAGE_VERIFY_ON_READ", false),
			VerifyMaxSize:      int64(env.Int("STORAGE_VERIFY_MAX_SIZE", 10*1024*1024)),
			VerifySample:       env.Bool("STORAGE_VERIFY_SAMPLE", true),
			CBFailureThreshold: env.Int("STORAGE_CB_FAILURE_THRESHOLD", 0),
			CBRecoveryTimeout:  env.Int("STORAGE_CB_RECOVERY_TIMEOUT", 0),
			CBHalfOpenMax:      env.Int("STORAGE_CB_HALF_OPEN_MAX", 0),
			CBEnabled:          env.Bool("STORAGE_CB_ENABLED", false),
			Local: LocalStorageConfig{
				Root:        getEnv("STORAGE_LOCAL_ROOT", "./var/objects"),
				PublicURL:   getEnv("STORAGE_LOCAL_PUBLIC_URL", ""),
				SignKey:     getEnv("STORAGE_LOCAL_SIGN_KEY", ""),
				SSEKey:      getEnv("STORAGE_LOCAL_SSE_KEY", ""),
				SSEKeyfile:  getEnv("STORAGE_LOCAL_SSE_KEYFILE", ""),
				SSEKeyURL:   getEnv("STORAGE_LOCAL_SSE_KEY_URL", ""),
				SSEKeyToken: getEnv("STORAGE_LOCAL_SSE_KEY_TOKEN", ""),
				SSEKMSURL:   getEnv("STORAGE_LOCAL_SSE_KMS_URL", ""),
				SSEKMSKeyID: getEnv("STORAGE_LOCAL_SSE_KMS_KEY_ID", ""),
				SSEKMSToken: getEnv("STORAGE_LOCAL_SSE_KMS_TOKEN", ""),
			},
			S3: S3StorageConfig{
				Endpoint:       getEnv("STORAGE_S3_ENDPOINT", ""),
				Region:         getEnv("STORAGE_S3_REGION", "us-east-1"),
				Bucket:         getEnv("STORAGE_S3_BUCKET", ""),
				AccessKey:      getEnv("STORAGE_S3_ACCESS_KEY", ""),
				SecretKey:      getEnv("STORAGE_S3_SECRET_KEY", ""),
				ForcePathStyle: env.Bool("STORAGE_S3_FORCE_PATH_STYLE", true),
			},
			OSS: OSSStorageConfig{
				Endpoint:  getEnv("STORAGE_OSS_ENDPOINT", ""),
				Bucket:    getEnv("STORAGE_OSS_BUCKET", ""),
				AccessKey: getEnv("STORAGE_OSS_ACCESS_KEY", ""),
				SecretKey: getEnv("STORAGE_OSS_SECRET_KEY", ""),
			},
			COS: COSStorageConfig{
				BucketURL: getEnv("STORAGE_COS_BUCKET_URL", ""),
				SecretID:  getEnv("STORAGE_COS_SECRET_ID", ""),
				SecretKey: getEnv("STORAGE_COS_SECRET_KEY", ""),
			},
		},
		DB: DBConfig{
			Driver: strings.ToLower(getEnv("DB_DRIVER", "sqlite")),
			DSN:    getEnv("DB_DSN", "file:./var/aero.db?_pragma=foreign_keys(1)"),
		},
		S3Compat: S3CompatConfig{
			Prefix: getEnv("S3_COMPAT_PREFIX", ""),
		},
		Events: EventsConfig{
			WebhookURL:    getEnv("EVENTS_WEBHOOK_URL", ""),
			WebhookSecret: getEnv("EVENTS_WEBHOOK_SECRET", ""),
			Transport:     strings.ToLower(getEnv("EVENTS_TRANSPORT", "")),
			TransportDSN:  getEnv("EVENTS_TRANSPORT_DSN", ""),
			SubBufferSize: env.Int("EVENTS_SUB_BUFFER", 0),
		},
		AI: AIConfig{
			Enabled:        env.Bool("AI_INDEX_ENABLED", false),
			Provider:       strings.ToLower(getEnv("AI_EMBED_PROVIDER", "hash")),
			Endpoint:       getEnv("AI_EMBED_ENDPOINT", ""),
			Model:          getEnv("AI_EMBED_MODEL", "text-embedding-3-small"),
			APIKey:         getEnv("AI_EMBED_API_KEY", ""),
			Dim:            env.Int("AI_EMBED_DIM", 256),
			HybridSearch:   env.Bool("AI_HYBRID_SEARCH", false),
			EmbedCacheSize: env.Int("AI_EMBED_CACHE_SIZE", 0),

			SearchCacheSize:       env.Int("AI_SEARCH_CACHE_SIZE", 0),
			SearchCacheTTLSeconds: env.Int("AI_SEARCH_CACHE_TTL_SECONDS", 30),
			ReindexStaleOnStart:   env.Bool("AI_REINDEX_STALE_ON_START", false),

			VectorBackend:    strings.ToLower(getEnv("AI_VECTOR_BACKEND", "")),
			VectorDSN:        getEnv("AI_VECTOR_DSN", ""),
			VectorURL:        getEnv("AI_VECTOR_URL", ""),
			VectorAPIKey:     getEnv("AI_VECTOR_API_KEY", ""),
			VectorCollection: getEnv("AI_VECTOR_COLLECTION", "aero_chunks"),
			LexicalBackend:   strings.ToLower(getEnv("AI_LEXICAL_BACKEND", "")),

			ExtractorEndpoint: getEnv("AI_EXTRACTOR_ENDPOINT", ""),
			ExtractorAPIKey:   getEnv("AI_EXTRACTOR_API_KEY", ""),

			ChatProvider: strings.ToLower(getEnv("AI_CHAT_PROVIDER", "")),
			ChatEndpoint: getEnv("AI_CHAT_ENDPOINT", ""),
			ChatModel:    getEnv("AI_CHAT_MODEL", "gpt-4o-mini"),
			ChatAPIKey:   getEnv("AI_CHAT_API_KEY", ""),

			ChatCostPromptPer1K:     env.Float("AI_COST_PROMPT_PER_1K", 0),
			ChatCostCompletionPer1K: env.Float("AI_COST_COMPLETION_PER_1K", 0),
			TenantDailyBudgetUSD:    env.Float("AI_TENANT_DAILY_BUDGET_USD", 0),
			PerTenantBudgets:        env.Bool("AI_PER_TENANT_BUDGETS", false),

			RerankProvider: strings.ToLower(getEnv("AI_RERANK_PROVIDER", "")),
			RerankEndpoint: getEnv("AI_RERANK_ENDPOINT", ""),
			RerankModel:    getEnv("AI_RERANK_MODEL", "bge-reranker-v2"),
			RerankAPIKey:   getEnv("AI_RERANK_API_KEY", ""),

			PIIScan:   env.Bool("AI_PII_SCAN", false),
			PIIRedact: env.Bool("AI_PII_REDACT", false),

			AgentMaxSteps: env.Int("AI_AGENT_MAX_STEPS", 4),
			ChunkWindow:   env.Int("AI_CHUNK_WINDOW", 600),
			ChunkOverlap:  env.Int("AI_CHUNK_OVERLAP", 80),
			DegradedMode:  env.Bool("AI_DEGRADED_MODE", false),
		},
		Auth: AuthConfig{
			Keys:                getEnv("AUTH_KEYS", ""),
			JWTSecret:           getEnv("AUTH_JWT_SECRET", ""),
			JWTIssuer:           getEnv("AUTH_JWT_ISSUER", ""),
			JWKSEndpoint:        getEnv("AUTH_JWKS_ENDPOINT", ""),
			JWKSKeyTTLSeconds:   env.Int("AUTH_JWKS_KEY_TTL", 3600),
			JWKSAudience:        getEnv("AUTH_JWKS_AUDIENCE", ""),
			JWKSTenantClaim:     getEnv("AUTH_JWKS_TENANT_CLAIM", "ten"),
			JWKSClientTenants:   splitMapping(getEnv("AUTH_JWKS_CLIENT_TENANTS", "")),
			JWKSClientTenantRaw: getEnv("AUTH_JWKS_CLIENT_TENANTS", ""),
			JWKSDefaultScopes:   splitCSV(getEnv("AUTH_JWKS_DEFAULT_SCOPES", "")),
			OIDCIssuer:          getEnv("AUTH_OIDC_ISSUER", ""),
			OIDCClientID:        getEnv("AUTH_OIDC_CLIENT_ID", ""),
			OIDCRedirectURI:     getEnv("AUTH_OIDC_REDIRECT_URI", ""),
			OIDCAuthorizeURL:    getEnv("AUTH_OIDC_AUTHORIZATION_ENDPOINT", ""),
			OIDCTokenURL:        getEnv("AUTH_OIDC_TOKEN_ENDPOINT", ""),
			OIDCScopes:          splitCSV(getEnv("AUTH_OIDC_SCOPES", "openid,profile,email")),
			AnonymousPublicRead: env.Bool("AUTH_ANONYMOUS_PUBLIC_READ", false),
			SigV4Credentials:    getEnv("S3_SIGV4_CREDENTIALS", ""),
			PresignSecret:       getEnv("AUTH_PRESIGN_SECRET", ""),
			PersistKeys:         env.Bool("AUTH_PERSIST_KEYS", false),
			KeyCacheTTLSeconds:  env.Int("AUTH_KEY_CACHE_TTL_SECONDS", 0),
		},
		Access: AccessConfig{
			Enabled:          env.Bool("ACCESS_CONTROL_ENABLED", false),
			DefaultPolicy:    strings.ToLower(getEnv("ACCESS_DEFAULT_POLICY", "deny")),
			ShareSecret:      getEnv("ACCESS_SHARE_SECRET", ""),
			PublicBaseURL:    strings.TrimRight(getEnv("ACCESS_PUBLIC_BASE_URL", ""), "/"),
			DeleteFailClosed: env.Bool("ACCESS_DELETE_FAIL_CLOSED", true),
		},
		Telemetry: TelemetryCfg{
			PrometheusEnabled: env.Bool("PROMETHEUS_ENABLED", false),
		},
		Billing:         billing,
		AuditGovernance: auditGovernance,
		EventOutbox:     eventOutbox,
		AuditSinkL2:     auditSinkL2,
		AuditSink:       auditSink,
		CORS: CORSCfg{
			AllowedOrigins: splitCSV(getEnv("CORS_ALLOWED_ORIGINS", "")),
			AllowedMethods: splitCSV(getEnv("CORS_ALLOWED_METHODS", "")),
			AllowedHeaders: splitCSV(getEnv("CORS_ALLOWED_HEADERS", "")),
			ExposeHeaders:  splitCSV(getEnv("CORS_EXPOSE_HEADERS", "")),
		},
		RateLimit: RateLimitCfg{
			RPS:        env.Float("RATE_LIMIT_RPS", 0),
			Burst:      env.Float("RATE_LIMIT_BURST", 0),
			AIRPS:      env.Float("AI_RATE_LIMIT_RPS", 0),
			AIBurst:    env.Float("AI_RATE_LIMIT_BURST", 0),
			AdminRPS:   env.Float("ADMIN_RATE_LIMIT_RPS", 0),
			AdminBurst: env.Float("ADMIN_RATE_LIMIT_BURST", 0),
		},
		Reconcile: ReconcileCfg{
			IntervalMinutes:     env.Int("RECONCILE_INTERVAL_MINUTES", 0),
			DeleteOrphanBlobs:   env.Bool("RECONCILE_DELETE_ORPHAN_BLOBS", false),
			OrphanGraceMinutes:  env.Int("RECONCILE_ORPHAN_GRACE_MINUTES", 60),
			Tenants:             reconcileTenants(),
			ClusterSingleton:    env.Bool("RECONCILE_CLUSTER_SINGLETON", false),
			ScrubEnabled:        env.Bool("RECONCILE_SCRUB_ENABLED", false),
			RetentionDays:       env.Int("RECONCILE_RETENTION_DAYS", 0),
			IdempotencyTTLHours: env.Int("IDEMPOTENCY_TTL_HOURS", 0),
			IdempotencyHashBody: env.Bool("IDEMPOTENCY_HASH_BODY", false),
			UploadGCHours:       env.Int("UPLOAD_GC_TTL_HOURS", 168),
			UploadGCEnable:      false, // enabled below when >0
		},
		Jobs: JobsCfg{
			Workers:  env.Int("JOBS_WORKERS", 4),
			MaxDepth: env.Int("JOBS_MAX_DEPTH", 0),
		},
		Antivirus: AntivirusCfg{
			Enabled:    env.Bool("AV_ENABLED", false),
			Provider:   strings.ToLower(getEnv("AV_PROVIDER", "signature")),
			Endpoint:   getEnv("AV_ENDPOINT", ""),
			APIKey:     getEnv("AV_API_KEY", ""),
			Quarantine: env.Bool("AV_QUARANTINE", false),
		},
		Replication: ReplicationCfg{
			Enabled:               env.Bool("REPLICATION_ENABLED", false),
			ResyncIntervalMinutes: env.Int("REPLICATION_RESYNC_INTERVAL_MINUTES", 0),
			Storage: StorageConfig{
				Backend: strings.ToLower(getEnv("REPLICATION_BACKEND", "local")),
				Local: LocalStorageConfig{
					Root:       getEnv("REPLICATION_LOCAL_ROOT", ""),
					SignKey:    getEnv("REPLICATION_LOCAL_SIGN_KEY", ""),
					SSEKey:     getEnv("REPLICATION_LOCAL_SSE_KEY", ""),
					SSEKeyfile: getEnv("REPLICATION_LOCAL_SSE_KEYFILE", ""),
				},
				S3: S3StorageConfig{
					Endpoint:       getEnv("REPLICATION_S3_ENDPOINT", ""),
					Region:         getEnv("REPLICATION_S3_REGION", "us-east-1"),
					Bucket:         getEnv("REPLICATION_S3_BUCKET", ""),
					AccessKey:      getEnv("REPLICATION_S3_ACCESS_KEY", ""),
					SecretKey:      getEnv("REPLICATION_S3_SECRET_KEY", ""),
					ForcePathStyle: env.Bool("REPLICATION_S3_FORCE_PATH_STYLE", true),
				},
			},
		},
		WebDAV: WebDAVCfg{Prefix: getEnv("WEBDAV_PREFIX", "")},
		WebUI:  WebUICfg{Enabled: env.Bool("WEBUI_ENABLED", true)},
	}

	// Enable upload GC when TTL is configured.
	cfg.Reconcile.UploadGCEnable = cfg.Reconcile.UploadGCHours > 0
	if err := env.Err(); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("invalid value %q for %s: %w", v, key, err)
	}
	return b, nil
}

func getEnvFloat(key string, def float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def, fmt.Errorf("invalid value %q for %s: %w", v, key, err)
	}
	return f, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitMapping(raw string) map[string]string {
	values := map[string]string{}
	for _, item := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return values
}

// reconcileTenants returns the tenants configured via RECONCILE_TENANTS as a
// trimmed, comma-separated list. If the env var is empty/unset it defaults to
// []string{"default"} so the field is always explicitly populated.
func reconcileTenants() []string {
	tenants := splitCSV(getEnv("RECONCILE_TENANTS", ""))
	if len(tenants) == 0 {
		return []string{"default"}
	}
	return tenants
}

func getEnvInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("invalid value %q for %s: %w", v, key, err)
	}
	return n, nil
}

// typedEnv records the first typed environment parse error while allowing the
// surrounding config literal to retain the existing source-order/default
// layout. Load returns the recorded error before validation, so malformed
// values can never silently become a valid default configuration.
type typedEnv struct{ err error }

func (e *typedEnv) remember(err error) {
	if e.err == nil && err != nil {
		e.err = err
	}
}

func (e *typedEnv) Bool(key string, def bool) bool {
	v, err := getEnvBool(key, def)
	e.remember(err)
	return v
}

func (e *typedEnv) Float(key string, def float64) float64 {
	v, err := getEnvFloat(key, def)
	e.remember(err)
	return v
}

func (e *typedEnv) Int(key string, def int) int {
	v, err := getEnvInt(key, def)
	e.remember(err)
	return v
}

func (e *typedEnv) Err() error { return e.err }

func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q", s)
	}
}
