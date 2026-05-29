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
	Addr     string
	LogLevel slog.Level
}

type StorageConfig struct {
	Backend string
	Local   LocalStorageConfig
	S3      S3StorageConfig
	OSS     OSSStorageConfig
	COS     COSStorageConfig
}

type OSSStorageConfig struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
}

type COSStorageConfig struct {
	BucketURL string
	SecretID  string
	SecretKey string
}

type LocalStorageConfig struct {
	Root      string
	PublicURL string
	SignKey   string
	SSEKey    string
}

type S3StorageConfig struct {
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
}

type DBConfig struct {
	Driver string
	DSN    string
}

type S3CompatConfig struct {
	Prefix string
}

type EventsConfig struct {
	WebhookURL    string
	WebhookSecret string
	// Cross-instance event transport (multi-replica). "" = in-process only;
	// "postgres" = LISTEN/NOTIFY bridge. Requires Postgres; opt-in.
	Transport    string
	TransportDSN string
}

type AIConfig struct {
	Enabled        bool
	Provider       string
	Endpoint       string
	Model          string
	APIKey         string
	Dim            int
	HybridSearch   bool
	EmbedCacheSize int // >0 wraps the embedder in a bounded in-memory cache

	SearchCacheSize       int  // >0 enables a bounded, TTL'd hot-result cache for identical repeated queries
	SearchCacheTTLSeconds int  // TTL bounding staleness of cached search results
	ReindexStaleOnStart   bool // re-index objects whose chunks use a different embed model (after embedder change)

	// Vector retrieval backend. "" = brute-force (default); "pgvector" = ANN via
	// Postgres pgvector. Requires Postgres + the vector extension; opt-in.
	VectorBackend string
	VectorDSN     string
	// Lexical retrieval backend. "" = in-process BM25 (default); "pgfts" =
	// Postgres full-text search. Reuses VectorDSN for its connection; opt-in.
	LexicalBackend string

	ExtractorEndpoint string
	ExtractorAPIKey   string

	ChatProvider string // "http" | "mock" | ""
	ChatEndpoint string
	ChatModel    string
	ChatAPIKey   string

	// Estimated cost accounting (USD per 1000 tokens; 0 = don't price).
	ChatCostPromptPer1K     float64
	ChatCostCompletionPer1K float64
	// Per-tenant daily AI spend cap (USD; 0 = unlimited). Enforced at the chat seam.
	TenantDailyBudgetUSD float64

	RerankProvider string // "http" | "heuristic" | ""
	RerankEndpoint string
	RerankModel    string
	RerankAPIKey   string

	PIIScan   bool
	PIIRedact bool
}

type AuthConfig struct {
	Keys                string
	JWTSecret           string
	AnonymousPublicRead bool
	SigV4Credentials    string // accessKey:secretKey:tenant[:scope+scope],...
	PersistKeys         bool   // back runtime API keys with the repository (hashed, survive restart)
	KeyCacheTTLSeconds  int    // >0 caches persisted-key lookups for this many seconds (revokes bounded by TTL across replicas)
}

type TelemetryCfg struct {
	PrometheusEnabled bool
}

type CORSCfg struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
}

type RateLimitCfg struct {
	RPS   float64
	Burst float64
}

type ReconcileCfg struct {
	IntervalMinutes     int
	DeleteOrphanBlobs   bool
	OrphanGraceMinutes  int
	Tenants             []string
	ClusterSingleton    bool // when true, only one replica runs the sweep (DB lease)
	RetentionDays       int  // >0 enables permanent GC of rows soft-deleted longer ago than this
	IdempotencyTTLHours int  // >0 enables GC of idempotency keys older than this
}

// JobsCfg controls the background job worker pool. Workers<=0 disables the
// pool, in which case the indexer falls back to processing events inline.
type JobsCfg struct {
	Workers  int
	MaxDepth int // >0 caps pending jobs (backpressure); Enqueue returns ErrQueueFull when reached
}

// AntivirusCfg controls malware scanning of uploaded objects (async via the job
// pool). Provider "signature" is the built-in dependency-free scanner; "http"
// defers to an external engine.
type AntivirusCfg struct {
	Enabled    bool
	Provider   string // signature | http
	Endpoint   string
	APIKey     string
	Quarantine bool // soft-delete infected objects
}

// ReplicationCfg configures asynchronous replication to a secondary storage
// backend (a different region/provider). Storage holds the replica target.
type ReplicationCfg struct {
	Enabled bool
	Storage StorageConfig
}

type WebDAVCfg struct {
	Prefix string // empty disables
}

type WebUICfg struct {
	Enabled bool
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	logLevel, err := parseLogLevel(getEnv("APP_LOG_LEVEL", "info"))
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		App: AppConfig{
			Addr:     getEnv("APP_ADDR", ":8080"),
			LogLevel: logLevel,
		},
		Storage: StorageConfig{
			Backend: strings.ToLower(getEnv("STORAGE_BACKEND", "local")),
			Local: LocalStorageConfig{
				Root:      getEnv("STORAGE_LOCAL_ROOT", "./var/objects"),
				PublicURL: getEnv("STORAGE_LOCAL_PUBLIC_URL", ""),
				SignKey:   getEnv("STORAGE_LOCAL_SIGN_KEY", ""),
				SSEKey:    getEnv("STORAGE_LOCAL_SSE_KEY", ""),
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

			VectorBackend:  strings.ToLower(getEnv("AI_VECTOR_BACKEND", "")),
			VectorDSN:      getEnv("AI_VECTOR_DSN", ""),
			LexicalBackend: strings.ToLower(getEnv("AI_LEXICAL_BACKEND", "")),

			ExtractorEndpoint: getEnv("AI_EXTRACTOR_ENDPOINT", ""),
			ExtractorAPIKey:   getEnv("AI_EXTRACTOR_API_KEY", ""),

			ChatProvider: strings.ToLower(getEnv("AI_CHAT_PROVIDER", "")),
			ChatEndpoint: getEnv("AI_CHAT_ENDPOINT", ""),
			ChatModel:    getEnv("AI_CHAT_MODEL", "gpt-4o-mini"),
			ChatAPIKey:   getEnv("AI_CHAT_API_KEY", ""),

			ChatCostPromptPer1K:     getEnvFloat("AI_COST_PROMPT_PER_1K", 0),
			ChatCostCompletionPer1K: getEnvFloat("AI_COST_COMPLETION_PER_1K", 0),
			TenantDailyBudgetUSD:    getEnvFloat("AI_TENANT_DAILY_BUDGET_USD", 0),

			RerankProvider: strings.ToLower(getEnv("AI_RERANK_PROVIDER", "")),
			RerankEndpoint: getEnv("AI_RERANK_ENDPOINT", ""),
			RerankModel:    getEnv("AI_RERANK_MODEL", "bge-reranker-v2"),
			RerankAPIKey:   getEnv("AI_RERANK_API_KEY", ""),

			PIIScan:   getEnvBool("AI_PII_SCAN", false),
			PIIRedact: getEnvBool("AI_PII_REDACT", false),
		},
		Auth: AuthConfig{
			Keys:                getEnv("AUTH_KEYS", ""),
			JWTSecret:           getEnv("AUTH_JWT_SECRET", ""),
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
		},
		RateLimit: RateLimitCfg{
			RPS:   getEnvFloat("RATE_LIMIT_RPS", 0),
			Burst: getEnvFloat("RATE_LIMIT_BURST", 0),
		},
		Reconcile: ReconcileCfg{
			IntervalMinutes:     getEnvInt("RECONCILE_INTERVAL_MINUTES", 0),
			DeleteOrphanBlobs:   getEnvBool("RECONCILE_DELETE_ORPHAN_BLOBS", false),
			OrphanGraceMinutes:  getEnvInt("RECONCILE_ORPHAN_GRACE_MINUTES", 60),
			Tenants:             reconcileTenants(),
			ClusterSingleton:    getEnvBool("RECONCILE_CLUSTER_SINGLETON", false),
			RetentionDays:       getEnvInt("RECONCILE_RETENTION_DAYS", 0),
			IdempotencyTTLHours: getEnvInt("IDEMPOTENCY_TTL_HOURS", 0),
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
					Root:    getEnv("REPLICATION_LOCAL_ROOT", ""),
					SignKey: getEnv("REPLICATION_LOCAL_SIGN_KEY", ""),
					SSEKey:  getEnv("REPLICATION_LOCAL_SSE_KEY", ""),
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

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
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
