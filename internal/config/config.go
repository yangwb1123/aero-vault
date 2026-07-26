package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	App         AppConfig
	Storage     StorageConfig
	DB          DBConfig
	S3Compat    S3CompatConfig
	Events      EventsConfig
	AI          AIConfig
	Auth        AuthConfig
	CORS        CORSCfg
	RateLimit   RateLimitCfg
	Reconcile   ReconcileCfg
	Jobs        JobsCfg
	Antivirus   AntivirusCfg
	Replication ReplicationCfg
	WebDAV      WebDAVCfg
	WebUI       WebUICfg
	Telemetry   TelemetryCfg
}

type AppConfig struct {
	Addr              string
	LogLevel          slog.Level
	TLSEnabled        bool   // APP_TLS_ENABLED
	TLSCertFile       string // APP_TLS_CERT_FILE
	TLSKeyFile        string // APP_TLS_KEY_FILE
	WriteTimeoutSec   int    // APP_WRITE_TIMEOUT; 0 = disabled
	IdleTimeoutSec    int    // APP_IDLE_TIMEOUT; 0 = disabled
	RequestTimeoutSec int    // REQUEST_TIMEOUT_SECONDS; 0 = disabled
	MaxInFlight       int    // MAX_INFLIGHT_REQUESTS; 0 = unlimited
	PerTenantMax      int    // PER_TENANT_CONCURRENCY_MAX; 0 = unlimited
	MaxBodySize       int    // APP_MAX_BODY_SIZE; max request body bytes (0 = unlimited)
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	logLevel, err := parseLogLevel(getEnv("APP_LOG_LEVEL", "info"))
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		App: AppConfig{
			Addr:              getEnv("APP_ADDR", ":8080"),
			LogLevel:          logLevel,
			TLSEnabled:        getEnvBool("APP_TLS_ENABLED", false),
			TLSCertFile:       getEnv("APP_TLS_CERT_FILE", ""),
			TLSKeyFile:        getEnv("APP_TLS_KEY_FILE", ""),
			WriteTimeoutSec:   getEnvInt("APP_WRITE_TIMEOUT", 60),
			IdleTimeoutSec:    getEnvInt("APP_IDLE_TIMEOUT", 120),
			RequestTimeoutSec: getEnvInt("REQUEST_TIMEOUT_SECONDS", 120),
			MaxInFlight:       getEnvInt("MAX_INFLIGHT_REQUESTS", 0),
			PerTenantMax:      getEnvInt("PER_TENANT_CONCURRENCY_MAX", 0),
			MaxBodySize:       getEnvInt("APP_MAX_BODY_SIZE", 0),
		},
		Storage: StorageConfig{
			Backend:            strings.ToLower(getEnv("STORAGE_BACKEND", "local")),
			SSERewrapOnStart:   getEnvBool("STORAGE_SSE_REWRAP_ON_START", false),
			DefaultClass:       getEnv("STORAGE_DEFAULT_CLASS", ""),
			ConnectTimeout:     getEnvInt("STORAGE_CONNECT_TIMEOUT", 5),
			ReadTimeout:        getEnvInt("STORAGE_READ_TIMEOUT", 30),
			WriteTimeout:       getEnvInt("STORAGE_WRITE_TIMEOUT", 30),
			VerifyOnRead:       getEnvBool("STORAGE_VERIFY_ON_READ", false),
			VerifyMaxSize:      int64(getEnvInt("STORAGE_VERIFY_MAX_SIZE", 10*1024*1024)),
			VerifySample:       getEnvBool("STORAGE_VERIFY_SAMPLE", true),
			CBFailureThreshold: getEnvInt("STORAGE_CB_FAILURE_THRESHOLD", 0),
			CBRecoveryTimeout:  getEnvInt("STORAGE_CB_RECOVERY_TIMEOUT", 0),
			CBHalfOpenMax:      getEnvInt("STORAGE_CB_HALF_OPEN_MAX", 0),
			CBEnabled:          getEnvBool("STORAGE_CB_ENABLED", false),
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
				ForcePathStyle: getEnvBool("STORAGE_S3_FORCE_PATH_STYLE", true),
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
			Prefix: getEnv("S3_COMPAT_PREFIX", "/s3"),
		},
		Events: EventsConfig{
			WebhookURL:    getEnv("EVENTS_WEBHOOK_URL", ""),
			WebhookSecret: getEnv("EVENTS_WEBHOOK_SECRET", ""),
			Transport:     strings.ToLower(getEnv("EVENTS_TRANSPORT", "")),
			TransportDSN:  getEnv("EVENTS_TRANSPORT_DSN", ""),
			SubBufferSize: getEnvInt("EVENTS_SUB_BUFFER", 0),
		},
		AI: AIConfig{
			Enabled:        getEnvBool("AI_INDEX_ENABLED", false),
			Provider:       strings.ToLower(getEnv("AI_EMBED_PROVIDER", "hash")),
			Endpoint:       getEnv("AI_EMBED_ENDPOINT", ""),
			Model:          getEnv("AI_EMBED_MODEL", "text-embedding-3-small"),
			APIKey:         getEnv("AI_EMBED_API_KEY", ""),
			Dim:            getEnvInt("AI_EMBED_DIM", 256),
			HybridSearch:   getEnvBool("AI_HYBRID_SEARCH", false),
			EmbedCacheSize: getEnvInt("AI_EMBED_CACHE_SIZE", 0),

			SearchCacheSize:       getEnvInt("AI_SEARCH_CACHE_SIZE", 0),
			SearchCacheTTLSeconds: getEnvInt("AI_SEARCH_CACHE_TTL_SECONDS", 30),
			ReindexStaleOnStart:   getEnvBool("AI_REINDEX_STALE_ON_START", false),

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

			ChatCostPromptPer1K:     getEnvFloat("AI_COST_PROMPT_PER_1K", 0),
			ChatCostCompletionPer1K: getEnvFloat("AI_COST_COMPLETION_PER_1K", 0),
			TenantDailyBudgetUSD:    getEnvFloat("AI_TENANT_DAILY_BUDGET_USD", 0),
			PerTenantBudgets:        getEnvBool("AI_PER_TENANT_BUDGETS", false),

			RerankProvider: strings.ToLower(getEnv("AI_RERANK_PROVIDER", "")),
			RerankEndpoint: getEnv("AI_RERANK_ENDPOINT", ""),
			RerankModel:    getEnv("AI_RERANK_MODEL", "bge-reranker-v2"),
			RerankAPIKey:   getEnv("AI_RERANK_API_KEY", ""),

			PIIScan:   getEnvBool("AI_PII_SCAN", false),
			PIIRedact: getEnvBool("AI_PII_REDACT", false),

			AgentMaxSteps: getEnvInt("AI_AGENT_MAX_STEPS", 4),
			ChunkWindow:   getEnvInt("AI_CHUNK_WINDOW", 600),
			ChunkOverlap:  getEnvInt("AI_CHUNK_OVERLAP", 80),
			DegradedMode:  getEnvBool("AI_DEGRADED_MODE", false),
		},
		Auth: AuthConfig{
			Keys:                getEnv("AUTH_KEYS", ""),
			JWTSecret:           getEnv("AUTH_JWT_SECRET", ""),
			JWTIssuer:           getEnv("AUTH_JWT_ISSUER", ""),
			JWKSEndpoint:        getEnv("AUTH_JWKS_ENDPOINT", ""),
			JWKSKeyTTLSeconds:   getEnvInt("AUTH_JWKS_KEY_TTL", 3600),
			AnonymousPublicRead: getEnvBool("AUTH_ANONYMOUS_PUBLIC_READ", false),
			SigV4Credentials:    getEnv("S3_SIGV4_CREDENTIALS", ""),
			PersistKeys:         getEnvBool("AUTH_PERSIST_KEYS", false),
			KeyCacheTTLSeconds:  getEnvInt("AUTH_KEY_CACHE_TTL_SECONDS", 0),
		},
		Telemetry: TelemetryCfg{
			PrometheusEnabled: getEnvBool("PROMETHEUS_ENABLED", false),
		},
		CORS: CORSCfg{
			AllowedOrigins: splitCSV(getEnv("CORS_ALLOWED_ORIGINS", "")),
			AllowedMethods: splitCSV(getEnv("CORS_ALLOWED_METHODS", "")),
			AllowedHeaders: splitCSV(getEnv("CORS_ALLOWED_HEADERS", "")),
			ExposeHeaders:  splitCSV(getEnv("CORS_EXPOSE_HEADERS", "")),
		},
		RateLimit: RateLimitCfg{
			RPS:        getEnvFloat("RATE_LIMIT_RPS", 0),
			Burst:      getEnvFloat("RATE_LIMIT_BURST", 0),
			AIRPS:      getEnvFloat("AI_RATE_LIMIT_RPS", 0),
			AIBurst:    getEnvFloat("AI_RATE_LIMIT_BURST", 0),
			AdminRPS:   getEnvFloat("ADMIN_RATE_LIMIT_RPS", 0),
			AdminBurst: getEnvFloat("ADMIN_RATE_LIMIT_BURST", 0),
		},
		Reconcile: ReconcileCfg{
			IntervalMinutes:     getEnvInt("RECONCILE_INTERVAL_MINUTES", 0),
			DeleteOrphanBlobs:   getEnvBool("RECONCILE_DELETE_ORPHAN_BLOBS", false),
			OrphanGraceMinutes:  getEnvInt("RECONCILE_ORPHAN_GRACE_MINUTES", 60),
			Tenants:             reconcileTenants(),
			ClusterSingleton:    getEnvBool("RECONCILE_CLUSTER_SINGLETON", false),
			ScrubEnabled:        getEnvBool("RECONCILE_SCRUB_ENABLED", false),
			RetentionDays:       getEnvInt("RECONCILE_RETENTION_DAYS", 0),
			IdempotencyTTLHours: getEnvInt("IDEMPOTENCY_TTL_HOURS", 0),
			IdempotencyHashBody: getEnvBool("IDEMPOTENCY_HASH_BODY", false),
			UploadGCHours:       getEnvInt("UPLOAD_GC_TTL_HOURS", 168),
			UploadGCEnable:      false, // enabled below when >0
		},
		Jobs: JobsCfg{
			Workers:  getEnvInt("JOBS_WORKERS", 4),
			MaxDepth: getEnvInt("JOBS_MAX_DEPTH", 0),
		},
		Antivirus: AntivirusCfg{
			Enabled:    getEnvBool("AV_ENABLED", false),
			Provider:   strings.ToLower(getEnv("AV_PROVIDER", "signature")),
			Endpoint:   getEnv("AV_ENDPOINT", ""),
			APIKey:     getEnv("AV_API_KEY", ""),
			Quarantine: getEnvBool("AV_QUARANTINE", false),
		},
		Replication: ReplicationCfg{
			Enabled: getEnvBool("REPLICATION_ENABLED", false),
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
					ForcePathStyle: getEnvBool("REPLICATION_S3_FORCE_PATH_STYLE", true),
				},
			},
		},
		WebDAV: WebDAVCfg{Prefix: getEnv("WEBDAV_PREFIX", "")},
		WebUI:  WebUICfg{Enabled: getEnvBool("WEBUI_ENABLED", true)},
	}

	// Enable upload GC when TTL is configured.
	cfg.Reconcile.UploadGCEnable = cfg.Reconcile.UploadGCHours > 0

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
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
	if err := c.validateTimeouts(); err != nil {
		return err
	}
	return c.validateRateLimits()
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

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getEnvFloat(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
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

func getEnvInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

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
