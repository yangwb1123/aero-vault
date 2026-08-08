package config

import (
	"strings"
	"testing"
	"time"
)

// ── AC-1: defaults (empty env reads as unset for getEnvInt/getEnvBool) ──────

func TestEventOutboxDefaults(t *testing.T) {
	for _, k := range []string{
		"EVENT_OUTBOX_ENABLED", "EVENT_OUTBOX_POLL_INTERVAL_MILLIS",
		"EVENT_OUTBOX_BATCH_SIZE", "EVENT_OUTBOX_CLAIM_TTL_SECONDS",
		"EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS", "EVENT_OUTBOX_MAX_ATTEMPTS",
		"EVENT_OUTBOX_DELIVERED_RETENTION_HOURS", "EVENT_OUTBOX_FAILED_RETENTION_HOURS",
	} {
		t.Setenv(k, "")
	}
	c := loadEventOutboxConfig()
	if !c.Enabled {
		t.Error("default Enabled = false, want true (always-on shipped behavior)")
	}
	if c.PollMilliseconds != 1000 {
		t.Errorf("default PollMilliseconds = %d, want 1000", c.PollMilliseconds)
	}
	if c.BatchSize != 32 {
		t.Errorf("default BatchSize = %d, want 32", c.BatchSize)
	}
	if c.ClaimTTLSeconds != 30 {
		t.Errorf("default ClaimTTLSeconds = %d, want 30", c.ClaimTTLSeconds)
	}
	if c.HTTPTimeoutSeconds != 5 {
		t.Errorf("default HTTPTimeoutSeconds = %d, want 5", c.HTTPTimeoutSeconds)
	}
	if c.MaxAttempts != 10 {
		t.Errorf("default MaxAttempts = %d, want 10", c.MaxAttempts)
	}
	if c.DeliveredRetentionHours != 24 {
		t.Errorf("default DeliveredRetentionHours = %d, want 24", c.DeliveredRetentionHours)
	}
	if c.FailedRetentionHours != 168 {
		t.Errorf("default FailedRetentionHours = %d, want 168", c.FailedRetentionHours)
	}
}

// ── AC-1: bounds validation, one var at a time (F3/F4 + boundary pins) ──────

// outboxValidEnv is the valid baseline for every outbox knob; each table row
// overrides exactly one var so failures isolate to the var under test.
var outboxValidEnv = map[string]string{
	"EVENT_OUTBOX_ENABLED":                   "true",
	"EVENT_OUTBOX_POLL_INTERVAL_MILLIS":      "1000",
	"EVENT_OUTBOX_BATCH_SIZE":                "32",
	"EVENT_OUTBOX_CLAIM_TTL_SECONDS":         "30",
	"EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS":      "5",
	"EVENT_OUTBOX_MAX_ATTEMPTS":              "10",
	"EVENT_OUTBOX_DELIVERED_RETENTION_HOURS": "24",
	"EVENT_OUTBOX_FAILED_RETENTION_HOURS":    "168",
}

func setOutboxEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	for k, v := range outboxValidEnv {
		t.Setenv(k, v)
	}
	for k, v := range overrides {
		t.Setenv(k, v)
	}
}

func TestEventOutboxValidation(t *testing.T) {
	// Load()-level rejections: values that survive withDefaults() and must
	// fail startup. Note 0 is NOT rejected here — Config.Validate() applies
	// withDefaults() first, so an explicit 0 reads as unset and falls back to
	// the default (pre-existing mechanism, same as every other knob); the
	// <= 0 boundary is pinned at the direct-Validate level below.
	rejects := []struct {
		name string
		env  map[string]string
		want string // error must name this env var
	}{
		{"poll 60001", map[string]string{"EVENT_OUTBOX_POLL_INTERVAL_MILLIS": "60001"}, "EVENT_OUTBOX_POLL_INTERVAL_MILLIS"},
		{"batch 501", map[string]string{"EVENT_OUTBOX_BATCH_SIZE": "501"}, "EVENT_OUTBOX_BATCH_SIZE"},
		{"timeout 30", map[string]string{"EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS": "30"}, "EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS"},
		// TTL == 2×timeout is rejected (strict > rule, cross-var; timeout=5 explicit).
		{"ttl 10 = 2×timeout", map[string]string{"EVENT_OUTBOX_CLAIM_TTL_SECONDS": "10"}, "EVENT_OUTBOX_CLAIM_TTL_SECONDS"},
		{"ttl 601", map[string]string{"EVENT_OUTBOX_CLAIM_TTL_SECONDS": "601"}, "EVENT_OUTBOX_CLAIM_TTL_SECONDS"},
		{"attempts 1001", map[string]string{"EVENT_OUTBOX_MAX_ATTEMPTS": "1001"}, "EVENT_OUTBOX_MAX_ATTEMPTS"},
		{"delivered -1", map[string]string{"EVENT_OUTBOX_DELIVERED_RETENTION_HOURS": "-1"}, "EVENT_OUTBOX_DELIVERED_RETENTION_HOURS"},
		{"delivered 8761", map[string]string{"EVENT_OUTBOX_DELIVERED_RETENTION_HOURS": "8761"}, "EVENT_OUTBOX_DELIVERED_RETENTION_HOURS"},
		{"failed -1", map[string]string{"EVENT_OUTBOX_FAILED_RETENTION_HOURS": "-1"}, "EVENT_OUTBOX_FAILED_RETENTION_HOURS"},
		{"failed 8761", map[string]string{"EVENT_OUTBOX_FAILED_RETENTION_HOURS": "8761"}, "EVENT_OUTBOX_FAILED_RETENTION_HOURS"},
	}
	for _, tc := range rejects {
		t.Run("reject "+tc.name, func(t *testing.T) {
			clearEnv(t)
			setOutboxEnv(t, tc.env)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() err = %v, want error naming %s", err, tc.want)
			}
		})
	}

	accepts := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, c *EventOutboxConfig)
	}{
		{"poll 60000", map[string]string{"EVENT_OUTBOX_POLL_INTERVAL_MILLIS": "60000"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.PollMilliseconds != 60000 {
				t.Errorf("PollMilliseconds = %d, want 60000", c.PollMilliseconds)
			}
		}},
		{"batch 500", map[string]string{"EVENT_OUTBOX_BATCH_SIZE": "500"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.BatchSize != 500 {
				t.Errorf("BatchSize = %d, want 500", c.BatchSize)
			}
		}},
		{"timeout 29", map[string]string{"EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS": "29", "EVENT_OUTBOX_CLAIM_TTL_SECONDS": "600"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.HTTPTimeoutSeconds != 29 {
				t.Errorf("HTTPTimeoutSeconds = %d, want 29", c.HTTPTimeoutSeconds)
			}
		}},
		// TTL == 2×timeout+1 pins the strict-> boundary (timeout=5 explicit).
		{"ttl 11 = 2×timeout+1", map[string]string{"EVENT_OUTBOX_CLAIM_TTL_SECONDS": "11"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.ClaimTTLSeconds != 11 {
				t.Errorf("ClaimTTLSeconds = %d, want 11", c.ClaimTTLSeconds)
			}
		}},
		{"ttl 600", map[string]string{"EVENT_OUTBOX_CLAIM_TTL_SECONDS": "600"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.ClaimTTLSeconds != 600 {
				t.Errorf("ClaimTTLSeconds = %d, want 600", c.ClaimTTLSeconds)
			}
		}},
		{"attempts 1000", map[string]string{"EVENT_OUTBOX_MAX_ATTEMPTS": "1000"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.MaxAttempts != 1000 {
				t.Errorf("MaxAttempts = %d, want 1000", c.MaxAttempts)
			}
		}},
		{"delivered 1", map[string]string{"EVENT_OUTBOX_DELIVERED_RETENTION_HOURS": "1"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.DeliveredRetentionHours != 1 {
				t.Errorf("DeliveredRetentionHours = %d, want 1", c.DeliveredRetentionHours)
			}
		}},
		{"delivered 8760", map[string]string{"EVENT_OUTBOX_DELIVERED_RETENTION_HOURS": "8760"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.DeliveredRetentionHours != 8760 {
				t.Errorf("DeliveredRetentionHours = %d, want 8760", c.DeliveredRetentionHours)
			}
			// No-overflow pin: 8760h must stay a positive time.Duration (F4).
			if time.Duration(8760)*time.Hour <= 0 {
				t.Error("8760h overflowed time.Duration")
			}
		}},
		{"failed 1", map[string]string{"EVENT_OUTBOX_FAILED_RETENTION_HOURS": "1"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.FailedRetentionHours != 1 {
				t.Errorf("FailedRetentionHours = %d, want 1", c.FailedRetentionHours)
			}
		}},
		{"failed 8760", map[string]string{"EVENT_OUTBOX_FAILED_RETENTION_HOURS": "8760"}, func(t *testing.T, c *EventOutboxConfig) {
			if c.FailedRetentionHours != 8760 {
				t.Errorf("FailedRetentionHours = %d, want 8760", c.FailedRetentionHours)
			}
		}},
	}
	for _, tc := range accepts {
		t.Run("accept "+tc.name, func(t *testing.T) {
			clearEnv(t)
			setOutboxEnv(t, tc.env)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() with %v failed: %v", tc.env, err)
			}
			tc.check(t, &cfg.EventOutbox)
		})
	}
}

// The <= 0 boundary of EventOutboxConfig.Validate() is unreachable through
// Load(): Config.Validate() applies withDefaults() first, so 0 reads as
// unset and becomes the default. Direct callers (hand-built configs) still
// hit the check, so it is pinned here at the unit boundary (F3).
func TestEventOutboxValidateDirectBoundaries(t *testing.T) {
	base := EventOutboxConfig{
		PollMilliseconds: 1000, BatchSize: 32, ClaimTTLSeconds: 30,
		HTTPTimeoutSeconds: 5, MaxAttempts: 10,
		DeliveredRetentionHours: 24, FailedRetentionHours: 168,
	}
	rejects := []struct {
		name   string
		mutate func(*EventOutboxConfig)
	}{
		{"poll 0", func(c *EventOutboxConfig) { c.PollMilliseconds = 0 }},
		{"batch 0", func(c *EventOutboxConfig) { c.BatchSize = 0 }},
		{"timeout 0", func(c *EventOutboxConfig) { c.HTTPTimeoutSeconds = 0 }},
		{"attempts 0", func(c *EventOutboxConfig) { c.MaxAttempts = 0 }},
		{"delivered 0", func(c *EventOutboxConfig) { c.DeliveredRetentionHours = 0 }},
		{"failed 0", func(c *EventOutboxConfig) { c.FailedRetentionHours = 0 }},
	}
	for _, tc := range rejects {
		t.Run("reject "+tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Errorf("%s must be rejected by EventOutboxConfig.Validate()", tc.name)
			}
		})
	}
	t.Run("accept valid base", func(t *testing.T) {
		if err := base.Validate(); err != nil {
			t.Fatalf("valid EventOutboxConfig rejected: %v", err)
		}
	})
}

// ── AC-1: Load()-level 0 reads as unset → default (withDefaults first) ──────

func TestEventOutboxLoad_ZeroReadsAsUnset(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want func(c *EventOutboxConfig) bool
	}{
		{"poll 0", map[string]string{"EVENT_OUTBOX_POLL_INTERVAL_MILLIS": "0"}, func(c *EventOutboxConfig) bool { return c.PollMilliseconds == 1000 }},
		{"delivered 0", map[string]string{"EVENT_OUTBOX_DELIVERED_RETENTION_HOURS": "0"}, func(c *EventOutboxConfig) bool { return c.DeliveredRetentionHours == 24 }},
		{"failed 0", map[string]string{"EVENT_OUTBOX_FAILED_RETENTION_HOURS": "0"}, func(c *EventOutboxConfig) bool { return c.FailedRetentionHours == 168 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			setOutboxEnv(t, tc.env)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if !tc.want(&cfg.EventOutbox) {
				t.Errorf("EventOutbox = %+v, want 0 read as unset → default", cfg.EventOutbox)
			}
		})
	}
}

// ── AC-1: Load()-level 0 reads as unset → default (withDefaults first) ──────
// ── AC-1: withDefaults contract (D4 at the unit boundary) ───────────────────

func TestEventOutboxWithDefaults(t *testing.T) {
	var c EventOutboxConfig
	d := c.withDefaults()
	if d.Enabled {
		t.Error("withDefaults() defaulted Enabled; Config.Validate() would flip an explicit EVENT_OUTBOX_ENABLED=false back to true (D4)")
	}
	if d.PollMilliseconds != 1000 || d.BatchSize != 32 || d.ClaimTTLSeconds != 30 ||
		d.HTTPTimeoutSeconds != 5 || d.MaxAttempts != 10 ||
		d.DeliveredRetentionHours != 24 || d.FailedRetentionHours != 168 {
		t.Errorf("withDefaults() numerics wrong: %+v", d)
	}
}

// ── AC-1: Config.Validate() never flips an explicit Enabled=false (F6) ──────

func TestEventOutboxValidate_PreservesExplicitDisabled(t *testing.T) {
	// Built on baseValid(): a bare zero Config{} fails earlier at
	// validateStorage, so it could never reach the EventOutbox section. Zero
	// numerics are fine — withDefaults() fills them, which also proves the
	// disabled config validates.
	c := baseValid()
	c.EventOutbox = EventOutboxConfig{Enabled: false}
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled EventOutbox must validate: %v", err)
	}
	if c.EventOutbox.Enabled {
		t.Fatal("Config.Validate() flipped explicit EVENT_OUTBOX_ENABLED=false back to true")
	}
	// Positive control: an explicit true survives too (guards a vacuous pass).
	c.EventOutbox.Enabled = true
	if err := c.Validate(); err != nil {
		t.Fatalf("enabled EventOutbox must validate: %v", err)
	}
	if !c.EventOutbox.Enabled {
		t.Fatal("Config.Validate() dropped explicit EVENT_OUTBOX_ENABLED=true")
	}
}

// ── AC-1: unparseable env falls back to the default (F2 / F11) ──────────────

func TestEventOutboxLoad_UnparseableEnvFallsBackToDefault(t *testing.T) {
	t.Run("enabled parse error falls back to default true", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("EVENT_OUTBOX_ENABLED", "tru") // genuine parse error ("1" parses true)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if !cfg.EventOutbox.Enabled {
			t.Fatal("unparseable EVENT_OUTBOX_ENABLED must fall back to default true")
		}
	})
	t.Run("parseable 0 disables", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("EVENT_OUTBOX_ENABLED", "0") // ParseBool accepts 0/1
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if cfg.EventOutbox.Enabled {
			t.Fatal("EVENT_OUTBOX_ENABLED=0 must disable the relay")
		}
	})
	t.Run("non-numeric retention falls back to the default", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("EVENT_OUTBOX_DELIVERED_RETENTION_HOURS", "abc")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if cfg.EventOutbox.DeliveredRetentionHours != 24 {
			t.Fatalf("DeliveredRetentionHours = %d, want default 24 (silent fallback, F11)", cfg.EventOutbox.DeliveredRetentionHours)
		}
	})
}

// ── AC-1: validation stays unconditional while the relay is disabled (F7) ───

func TestEventOutboxValidate_DisabledStillValidatesL2(t *testing.T) {
	c := baseValid()
	c.EventOutbox.Enabled = false
	c.AuditSinkL2.Endpoint = "not-a-url"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "AUDIT_SINK_L2_ENDPOINT") {
		t.Fatalf("disabled relay must still fail startup on a malformed L2 endpoint, got %v", err)
	}
	// Positive control: with L2 off (empty endpoint) the disabled config validates.
	c.AuditSinkL2.Endpoint = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled relay with L2 off must validate: %v", err)
	}
}
