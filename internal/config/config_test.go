package config

import (
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
)

// --- helpers: getEnv / getEnvBool / getEnvInt / getEnvFloat / splitCSV ---

func TestGetEnv(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		// Key intentionally not set.
		if got := getEnv("AERO_TEST_UNSET_X", "def"); got != "def" {
			t.Fatalf("want default %q, got %q", "def", got)
		}
	})
	t.Run("empty value returns default", func(t *testing.T) {
		t.Setenv("AERO_TEST_EMPTY", "")
		if got := getEnv("AERO_TEST_EMPTY", "def"); got != "def" {
			t.Fatalf("empty env should fall back to default, got %q", got)
		}
	})
	t.Run("set value wins", func(t *testing.T) {
		t.Setenv("AERO_TEST_SET", "value")
		if got := getEnv("AERO_TEST_SET", "def"); got != "value" {
			t.Fatalf("want %q, got %q", "value", got)
		}
	})
}

func TestGetEnvBool(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		def  bool
		want bool
		err  bool
	}{
		{"unset uses default true", false, "", true, true, false},
		{"unset uses default false", false, "", false, false, false},
		{"empty uses default", true, "", true, true, false},
		{"true literal", true, "true", false, true, false},
		{"1 is true", true, "1", false, true, false},
		{"false literal", true, "false", true, false, false},
		{"0 is false", true, "0", true, false, false},
		{"unparseable returns error", true, "notabool", true, true, true},
		{"unparseable false returns error", true, "yepnope", false, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv("AERO_TEST_BOOL", c.val)
			}
			key := "AERO_TEST_BOOL"
			if !c.set {
				key = "AERO_TEST_BOOL_UNSET"
			}
			got, err := getEnvBool(key, c.def)
			if (err != nil) != c.err {
				t.Fatalf("getEnvBool(%q=%q) error = %v, want error=%v", key, c.val, err, c.err)
			}
			if got != c.want {
				t.Fatalf("getEnvBool(%q=%q, def=%v) = %v, want %v", key, c.val, c.def, got, c.want)
			}
		})
	}
}

func TestGetEnvInt(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		def  int
		want int
		err  bool
	}{
		{"unset uses default", false, "", 7, 7, false},
		{"empty uses default", true, "", 7, 7, false},
		{"parses positive", true, "42", 0, 42, false},
		{"parses negative", true, "-3", 0, -3, false},
		{"unparseable returns error", true, "12.5", 9, 9, true},
		{"garbage returns error", true, "abc", 9, 9, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := "AERO_TEST_INT"
			if c.set {
				t.Setenv(key, c.val)
			} else {
				key = "AERO_TEST_INT_UNSET"
			}
			got, err := getEnvInt(key, c.def)
			if (err != nil) != c.err {
				t.Fatalf("getEnvInt(%q=%q) error = %v, want error=%v", key, c.val, err, c.err)
			}
			if got != c.want {
				t.Fatalf("getEnvInt(%q=%q, def=%d) = %d, want %d", key, c.val, c.def, got, c.want)
			}
		})
	}
}

func TestGetEnvFloat(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		def  float64
		want float64
		err  bool
	}{
		{"unset uses default", false, "", 1.5, 1.5, false},
		{"empty uses default", true, "", 1.5, 1.5, false},
		{"parses float", true, "3.25", 0, 3.25, false},
		{"parses integer-like", true, "10", 0, 10, false},
		{"unparseable returns error", true, "x", 2.0, 2.0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := "AERO_TEST_FLOAT"
			if c.set {
				t.Setenv(key, c.val)
			} else {
				key = "AERO_TEST_FLOAT_UNSET"
			}
			got, err := getEnvFloat(key, c.def)
			if (err != nil) != c.err {
				t.Fatalf("getEnvFloat(%q=%q) error = %v, want error=%v", key, c.val, err, c.err)
			}
			if got != c.want {
				t.Fatalf("getEnvFloat(%q=%q, def=%v) = %v, want %v", key, c.val, c.def, got, c.want)
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty is nil", "", nil},
		{"single", "a", []string{"a"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"trims whitespace", " a , b ,c ", []string{"a", "b", "c"}},
		{"drops empty fields", "a,,b,  ,c", []string{"a", "b", "c"}},
		{"only commas is nil-ish empty", ", ,", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitCSV(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("splitCSV(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"Error", slog.LevelError, false},
		{"", 0, true},
		{"verbose", 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseLogLevel(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseLogLevel(%q) expected error, got level %v", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLogLevel(%q) unexpected error: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("parseLogLevel(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// --- Validate() ---

// baseValid returns a Config that passes Validate(): local backend + sqlite.
func baseValid() *Config {
	c := &Config{}
	c.Storage.Backend = "local"
	c.Storage.Local.Root = "./var/objects"
	c.DB.Driver = "sqlite"
	c.DB.DSN = "file:./var/aero.db"
	return c
}

func TestValidate_OK(t *testing.T) {
	if err := baseValid().Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

// TestValidateThumbnailCacheBytes pins the THUMBNAIL_CACHE_BYTES kill-switch:
// 0 (disabled default) and positive budgets are accepted; negatives are
// rejected at startup validation.
func TestValidateThumbnailCacheBytes(t *testing.T) {
	c := baseValid()
	if err := c.Validate(); err != nil {
		t.Fatalf("default (0 = disabled) must validate: %v", err)
	}
	c.App.ThumbnailCacheBytes = 32 << 20
	if err := c.Validate(); err != nil {
		t.Fatalf("positive budget must validate: %v", err)
	}
	c.App.ThumbnailCacheBytes = -1
	if err := c.Validate(); err == nil {
		t.Fatal("negative THUMBNAIL_CACHE_BYTES must be rejected")
	}
}

// TestValidateThumbnailCacheTTL (AC-4) pins the THUMBNAIL_CACHE_TTL knob:
// 0 (disabled default) and positive values validate, negatives are rejected
// at startup, the wiring loads the env var exactly like THUMBNAIL_CACHE_BYTES,
// and — the deliberate divergence from AI_SEARCH_CACHE_TTL_SECONDS — a
// positive TTL with a disabled byte-budget cache is harmless and validates
// (FM-5). An absurdly large TTL (beyond one year) is rejected to fail fast
// on time.Duration overflow at the wiring site (perf F3).
func TestValidateThumbnailCacheTTL(t *testing.T) {
	c := baseValid()
	if err := c.Validate(); err != nil {
		t.Fatalf("default (0 = disabled) must validate: %v", err)
	}
	c.App.ThumbnailCacheTTL = 3600
	if err := c.Validate(); err != nil {
		t.Fatalf("positive TTL must validate: %v", err)
	}
	// Deliberate divergence: TTL > 0 with BYTES = 0 (cache disabled) is a
	// harmless pass-through no-op — it must NOT be rejected (unlike the AI
	// search cache cross-constraint).
	c.App.ThumbnailCacheBytes = 0
	if err := c.Validate(); err != nil {
		t.Fatalf("positive TTL with disabled byte-budget cache must validate: %v", err)
	}
	c.App.ThumbnailCacheTTL = -1
	if err := c.Validate(); err == nil {
		t.Fatal("negative THUMBNAIL_CACHE_TTL must be rejected")
	}
	c.App.ThumbnailCacheTTL = 31536001
	if err := c.Validate(); err == nil {
		t.Fatal("THUMBNAIL_CACHE_TTL beyond one year must be rejected (time.Duration overflow guard)")
	}

	// Wiring clause: the env var is loaded into cfg.App.ThumbnailCacheTTL,
	// mirroring THUMBNAIL_CACHE_BYTES; unset/empty falls back to 0.
	t.Setenv("THUMBNAIL_CACHE_TTL", "3600")
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load with THUMBNAIL_CACHE_TTL set: %v", err)
	}
	if loaded.App.ThumbnailCacheTTL != 3600 {
		t.Fatalf("wired ThumbnailCacheTTL = %d, want 3600", loaded.App.ThumbnailCacheTTL)
	}
	t.Setenv("THUMBNAIL_CACHE_TTL", "")
	loaded, err = Load()
	if err != nil {
		t.Fatalf("Load with THUMBNAIL_CACHE_TTL empty: %v", err)
	}
	if loaded.App.ThumbnailCacheTTL != 0 {
		t.Fatalf("wired ThumbnailCacheTTL = %d, want 0 (default) when unset/empty", loaded.App.ThumbnailCacheTTL)
	}
}

func TestValidateThumbnailPerTenantDecodeSlots(t *testing.T) {
	c := baseValid()
	if c.App.ThumbnailPerTenantDecodeSlots != 0 {
		t.Fatalf("default ThumbnailPerTenantDecodeSlots = %d, want 0", c.App.ThumbnailPerTenantDecodeSlots)
	}
	c.App.ThumbnailPerTenantDecodeSlots = 2
	if err := c.Validate(); err != nil {
		t.Fatalf("positive per-tenant decode slots rejected: %v", err)
	}
	c.App.ThumbnailPerTenantDecodeSlots = -1
	if err := c.Validate(); err == nil {
		t.Fatal("negative per-tenant decode slots must be rejected")
	}
	t.Setenv("THUMBNAIL_PER_TENANT_DECODE_SLOTS", "3")
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load with per-tenant decode slots: %v", err)
	}
	if loaded.App.ThumbnailPerTenantDecodeSlots != 3 {
		t.Fatalf("loaded per-tenant decode slots = %d, want 3", loaded.App.ThumbnailPerTenantDecodeSlots)
	}
}

func TestValidate_Storage(t *testing.T) {
	t.Run("local requires root", func(t *testing.T) {
		c := baseValid()
		c.Storage.Local.Root = ""
		if err := c.Validate(); err == nil {
			t.Fatal("expected error when STORAGE_LOCAL_ROOT empty")
		}
	})
	t.Run("s3 requires bucket", func(t *testing.T) {
		c := baseValid()
		c.Storage.Backend = "s3"
		c.Storage.S3.Bucket = ""
		if err := c.Validate(); err == nil {
			t.Fatal("expected error when s3 bucket empty")
		}
		c.Storage.S3.Bucket = "my-bucket"
		if err := c.Validate(); err != nil {
			t.Fatalf("s3 with bucket should be valid, got: %v", err)
		}
	})
	t.Run("oss requires endpoint and bucket", func(t *testing.T) {
		c := baseValid()
		c.Storage.Backend = "oss"
		// neither set
		if err := c.Validate(); err == nil {
			t.Fatal("expected error when oss endpoint+bucket empty")
		}
		// only endpoint
		c.Storage.OSS.Endpoint = "oss.example.com"
		if err := c.Validate(); err == nil {
			t.Fatal("expected error when oss bucket still empty")
		}
		// only bucket
		c.Storage.OSS.Endpoint = ""
		c.Storage.OSS.Bucket = "b"
		if err := c.Validate(); err == nil {
			t.Fatal("expected error when oss endpoint still empty")
		}
		// both
		c.Storage.OSS.Endpoint = "oss.example.com"
		if err := c.Validate(); err != nil {
			t.Fatalf("oss with endpoint+bucket should be valid, got: %v", err)
		}
	})
	t.Run("cos requires bucket url", func(t *testing.T) {
		c := baseValid()
		c.Storage.Backend = "cos"
		if err := c.Validate(); err == nil {
			t.Fatal("expected error when cos bucket url empty")
		}
		c.Storage.COS.BucketURL = "https://b-123.cos.ap.myqcloud.com"
		if err := c.Validate(); err != nil {
			t.Fatalf("cos with bucket url should be valid, got: %v", err)
		}
	})
	t.Run("unknown backend errors", func(t *testing.T) {
		c := baseValid()
		c.Storage.Backend = "minio"
		err := c.Validate()
		if err == nil {
			t.Fatal("expected error for unknown backend")
		}
		if want := `unknown STORAGE_BACKEND "minio"`; err.Error() != want {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	})
}

func TestValidate_DB(t *testing.T) {
	t.Run("postgres ok", func(t *testing.T) {
		c := baseValid()
		c.DB.Driver = "postgres"
		if err := c.Validate(); err != nil {
			t.Fatalf("postgres should be valid, got: %v", err)
		}
	})
	t.Run("unknown driver errors", func(t *testing.T) {
		c := baseValid()
		c.DB.Driver = "mysql"
		err := c.Validate()
		if err == nil {
			t.Fatal("expected error for unknown driver")
		}
		if want := `unknown DB_DRIVER "mysql"`; err.Error() != want {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	})
	t.Run("empty DSN errors", func(t *testing.T) {
		c := baseValid()
		c.DB.DSN = ""
		if err := c.Validate(); err == nil {
			t.Fatal("expected error when DSN empty")
		}
	})
}

func TestValidate_AIHTTPRequiresEndpoint(t *testing.T) {
	c := baseValid()
	c.AI.Enabled = true
	c.AI.Provider = "http"
	c.AI.Endpoint = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected error: http embed provider needs endpoint")
	}
	c.AI.Endpoint = "http://embed.local"
	if err := c.Validate(); err != nil {
		t.Fatalf("http embed provider with endpoint should be valid, got: %v", err)
	}

	// Disabled AI never requires the endpoint even with http provider.
	c.AI.Enabled = false
	c.AI.Endpoint = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled AI should not require endpoint, got: %v", err)
	}

	// Non-http provider never requires the endpoint.
	c.AI.Enabled = true
	c.AI.Provider = "hash"
	c.AI.Endpoint = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("hash provider should not require endpoint, got: %v", err)
	}
}

func TestValidateAIAccountingAndCacheValues(t *testing.T) {
	tests := []func(*Config){
		func(c *Config) { c.AI.ChatCostPromptPer1K = -1 },
		func(c *Config) { c.AI.ChatCostCompletionPer1K = -1 },
		func(c *Config) { c.AI.TenantDailyBudgetUSD = -1 },
		func(c *Config) { c.AI.AgentMaxSteps = -1 },
		func(c *Config) { c.AI.SearchCacheSize = -1 },
		func(c *Config) { c.AI.SearchCacheTTLSeconds = -1 },
		func(c *Config) {
			c.AI.SearchCacheSize = 10
			c.AI.SearchCacheTTLSeconds = 0
		},
	}
	for index, mutate := range tests {
		config := baseValid()
		mutate(config)
		if err := config.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error", index)
		}
	}
}

// --- Load() ---

// clearEnv unsets every env var Load() reads so a polluted host environment
// can't leak into the test. t.Setenv restores originals at test end.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"APP_LOG_LEVEL", "APP_ADDR",
		"STORAGE_BACKEND", "STORAGE_LOCAL_ROOT", "STORAGE_LOCAL_PUBLIC_URL",
		"STORAGE_S3_BUCKET", "STORAGE_S3_ENDPOINT", "STORAGE_S3_REGION",
		"STORAGE_S3_FORCE_PATH_STYLE",
		"STORAGE_OSS_ENDPOINT", "STORAGE_OSS_BUCKET",
		"STORAGE_COS_BUCKET_URL",
		"DB_DRIVER", "DB_DSN",
		"S3_COMPAT_PREFIX",
		"AI_INDEX_ENABLED", "AI_EMBED_PROVIDER", "AI_EMBED_ENDPOINT", "AI_EMBED_DIM",
		"CORS_ALLOWED_ORIGINS", "CORS_ALLOWED_METHODS",
		"RATE_LIMIT_RPS", "RATE_LIMIT_BURST",
		"RECONCILE_INTERVAL_MINUTES", "RECONCILE_DELETE_ORPHAN_BLOBS",
		"RECONCILE_ORPHAN_GRACE_MINUTES", "JOBS_WORKERS",
		"REPLICATION_ENABLED", "REPLICATION_RESYNC_INTERVAL_MINUTES",
		"WEBUI_ENABLED", "WEBDAV_PREFIX",
		"PROMETHEUS_ENABLED",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with defaults failed: %v", err)
	}
	if cfg.App.Addr != ":8080" {
		t.Errorf("default Addr = %q, want :8080", cfg.App.Addr)
	}
	if cfg.App.LogLevel != slog.LevelInfo {
		t.Errorf("default LogLevel = %v, want info", cfg.App.LogLevel)
	}
	if cfg.Storage.Backend != "local" {
		t.Errorf("default Backend = %q, want local", cfg.Storage.Backend)
	}
	if cfg.Storage.Local.Root != "./var/objects" {
		t.Errorf("default Local.Root = %q", cfg.Storage.Local.Root)
	}
	if cfg.DB.Driver != "sqlite" {
		t.Errorf("default DB.Driver = %q, want sqlite", cfg.DB.Driver)
	}
	if cfg.S3Compat.Prefix != "" {
		t.Errorf("default S3Compat.Prefix = %q, want disabled", cfg.S3Compat.Prefix)
	}
	if cfg.Storage.S3.ForcePathStyle != true {
		t.Errorf("default S3.ForcePathStyle = %v, want true", cfg.Storage.S3.ForcePathStyle)
	}
	if cfg.Jobs.Workers != 4 {
		t.Errorf("default Jobs.Workers = %d, want 4", cfg.Jobs.Workers)
	}
	if cfg.Replication.ResyncIntervalMinutes != 0 {
		t.Errorf("default Replication.ResyncIntervalMinutes = %d, want 0",
			cfg.Replication.ResyncIntervalMinutes)
	}
	if cfg.AI.Dim != 256 {
		t.Errorf("default AI.Dim = %d, want 256", cfg.AI.Dim)
	}
	if !cfg.WebUI.Enabled {
		t.Errorf("default WebUI.Enabled = false, want true")
	}
	if cfg.RateLimit.RPS != 0 || cfg.RateLimit.Burst != 0 {
		t.Errorf("default RateLimit = %+v, want zero", cfg.RateLimit)
	}
	if cfg.App.ThumbnailCacheBytes != 0 {
		t.Errorf("default ThumbnailCacheBytes = %d, want 0 (disabled)", cfg.App.ThumbnailCacheBytes)
	}
}

func TestLoad_S3CompatPrefixOptIn(t *testing.T) {
	t.Run("unset disables gateway", func(t *testing.T) {
		clearEnv(t)
		if err := os.Unsetenv("S3_COMPAT_PREFIX"); err != nil {
			t.Fatalf("unset S3_COMPAT_PREFIX: %v", err)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if cfg.S3Compat.Prefix != "" {
			t.Fatalf("unset prefix = %q, want disabled", cfg.S3Compat.Prefix)
		}
	})

	t.Run("explicit empty disables gateway", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("S3_COMPAT_PREFIX", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if cfg.S3Compat.Prefix != "" {
			t.Fatalf("empty prefix = %q, want disabled", cfg.S3Compat.Prefix)
		}
	})

	t.Run("non-empty prefix enables gateway", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("S3_COMPAT_PREFIX", "/s3")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if cfg.S3Compat.Prefix != "/s3" {
			t.Fatalf("configured prefix = %q, want /s3", cfg.S3Compat.Prefix)
		}
	})
}

func TestLoad_OverridesAndLowercasing(t *testing.T) {
	clearEnv(t)
	t.Setenv("STORAGE_BACKEND", "S3") // mixed case -> lowercased
	t.Setenv("STORAGE_S3_BUCKET", "bkt")
	t.Setenv("DB_DRIVER", "Postgres")
	t.Setenv("DB_DSN", "postgres://localhost/aero")
	t.Setenv("APP_ADDR", ":9090")
	t.Setenv("APP_LOG_LEVEL", "debug")
	t.Setenv("RATE_LIMIT_RPS", "10.5")
	t.Setenv("RATE_LIMIT_BURST", "20")
	t.Setenv("JOBS_WORKERS", "8")
	t.Setenv("REPLICATION_RESYNC_INTERVAL_MINUTES", "30")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.com, https://b.com")
	t.Setenv("WEBUI_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Storage.Backend != "s3" {
		t.Errorf("Backend = %q, want s3 (lowercased)", cfg.Storage.Backend)
	}
	if cfg.DB.Driver != "postgres" {
		t.Errorf("Driver = %q, want postgres (lowercased)", cfg.DB.Driver)
	}
	if cfg.App.Addr != ":9090" {
		t.Errorf("Addr = %q", cfg.App.Addr)
	}
	if cfg.App.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.App.LogLevel)
	}
	if cfg.RateLimit.RPS != 10.5 || cfg.RateLimit.Burst != 20 {
		t.Errorf("RateLimit = %+v", cfg.RateLimit)
	}
	if cfg.Jobs.Workers != 8 {
		t.Errorf("Workers = %d", cfg.Jobs.Workers)
	}
	if cfg.Replication.ResyncIntervalMinutes != 30 {
		t.Errorf("Replication resync interval = %d, want 30", cfg.Replication.ResyncIntervalMinutes)
	}
	if want := []string{"https://a.com", "https://b.com"}; !reflect.DeepEqual(cfg.CORS.AllowedOrigins, want) {
		t.Errorf("CORS origins = %#v, want %#v", cfg.CORS.AllowedOrigins, want)
	}
	if cfg.WebUI.Enabled {
		t.Errorf("WebUI.Enabled = true, want false")
	}
}

func TestLoad_InvalidLogLevel(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_LOG_LEVEL", "loud")
	if _, err := Load(); err == nil {
		t.Fatal("expected Load() to fail on invalid log level")
	}
}

func TestLoad_InvalidTypedEnvFailsFast(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"bool", "APP_TLS_ENABLED", "yes"},
		{"int", "APP_MAX_BODY_SIZE", "30m"},
		{"float", "AI_TENANT_DAILY_BUDGET_USD", "100USD"},
		{"billing bool", "BILLING_ENABLED", "tru"},
		{"audit governance int", "AUDIT_GOVERNANCE_MAX_LAG_SECONDS", "900s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil ||
				!strings.Contains(err.Error(), tc.key) ||
				!strings.Contains(err.Error(), tc.value) {
				t.Fatalf("Load() error = %v, want %s and %q", err, tc.key, tc.value)
			}
		})
	}
}

func TestLoad_ValidationPropagates(t *testing.T) {
	clearEnv(t)
	// s3 backend without a bucket must fail validation through Load().
	t.Setenv("STORAGE_BACKEND", "s3")
	if _, err := Load(); err == nil {
		t.Fatal("expected Load() to fail: s3 backend without bucket")
	}
}
