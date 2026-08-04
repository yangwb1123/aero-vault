package config

type DBConfig struct {
	Driver string
	DSN    string
}

type S3CompatConfig struct {
	Prefix string
}

type AccessConfig struct {
	Enabled       bool
	DefaultPolicy string
	ShareSecret   string
	PublicBaseURL string
}

type EventsConfig struct {
	WebhookURL    string
	WebhookSecret string
	// Cross-instance event transport (multi-replica). "" = in-process only;
	// "postgres" = LISTEN/NOTIFY bridge. Requires Postgres; opt-in.
	Transport     string
	TransportDSN  string
	SubBufferSize int // EVENTS_SUB_BUFFER; per-subscriber channel depth (0 = default 64)
}

type ReconcileCfg struct {
	IntervalMinutes     int
	DeleteOrphanBlobs   bool
	OrphanGraceMinutes  int
	Tenants             []string
	ClusterSingleton    bool // when true, only one replica runs the sweep (DB lease)
	RetentionDays       int  // >0 enables permanent GC of rows soft-deleted longer ago than this
	IdempotencyTTLHours int  // >0 enables GC of idempotency keys older than this
	IdempotencyHashBody bool // fold a request-body hash into /v1 idempotency fingerprints (catches same-key/different-bytes)
	ScrubEnabled        bool // RECONCILE_SCRUB_ENABLED; verify stored MD5 checksums
	// Upload GC — multipart upload cleanup
	UploadGCHours  int  // UPLOAD_GC_TTL_HOURS; >0 enables upload GC (default 168 = 7 days)
	UploadGCEnable bool // set automatically when UploadGCHours > 0; also gates the interval
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

type TelemetryCfg struct {
	PrometheusEnabled bool
}
