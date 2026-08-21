package config

import (
	"errors"
	"fmt"
)

// EventOutboxConfig configures the deletion transactional-outbox relay
// (cmd/server startEventOutboxRelay). Defaults mirror the billing/audit
// outbox precedents; the relay runs by default (core deletion atomicity is
// not gated — EVENT_OUTBOX_ENABLED=false stops only the relay loop, never
// the transactional enqueue) and is a silent no-op without notification
// rules.
type EventOutboxConfig struct {
	Enabled bool // EVENT_OUTBOX_ENABLED (kill-switch for the relay loop)

	PollMilliseconds        int // EVENT_OUTBOX_POLL_INTERVAL_MILLIS
	BatchSize               int // EVENT_OUTBOX_BATCH_SIZE
	ClaimTTLSeconds         int // EVENT_OUTBOX_CLAIM_TTL_SECONDS
	HTTPTimeoutSeconds      int // EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS
	MaxAttempts             int // EVENT_OUTBOX_MAX_ATTEMPTS
	DeliveredRetentionHours int // EVENT_OUTBOX_DELIVERED_RETENTION_HOURS
	FailedRetentionHours    int // EVENT_OUTBOX_FAILED_RETENTION_HOURS
}

func loadEventOutboxConfig() (EventOutboxConfig, error) {
	env := &typedEnv{}
	cfg := EventOutboxConfig{
		Enabled:                 env.Bool("EVENT_OUTBOX_ENABLED", true),
		PollMilliseconds:        env.Int("EVENT_OUTBOX_POLL_INTERVAL_MILLIS", 1000),
		BatchSize:               env.Int("EVENT_OUTBOX_BATCH_SIZE", 32),
		ClaimTTLSeconds:         env.Int("EVENT_OUTBOX_CLAIM_TTL_SECONDS", 30),
		HTTPTimeoutSeconds:      env.Int("EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS", 5),
		MaxAttempts:             env.Int("EVENT_OUTBOX_MAX_ATTEMPTS", 10),
		DeliveredRetentionHours: env.Int("EVENT_OUTBOX_DELIVERED_RETENTION_HOURS", 24),
		FailedRetentionHours:    env.Int("EVENT_OUTBOX_FAILED_RETENTION_HOURS", 168),
	}
	if err := env.Err(); err != nil {
		return EventOutboxConfig{}, err
	}
	return cfg, nil
}

// withDefaults fills zero numeric fields with the billing-mirrored defaults,
// so a hand-built zero config validates like the env-loaded one (Load always
// populates every field).
//
// Enabled is deliberately NOT defaulted (D4): false is a valid explicit
// value, and Config.Validate() applies withDefaults unconditionally —
// treating false as "unset" would silently flip an operator's
// EVENT_OUTBOX_ENABLED=false back to true at every validation. Numeric
// fields are safe because 0 is never a valid explicit value (all bounds
// ≥ 1). Asymmetry (harmless today): a hand-built zero Config{} yields a
// relay-off EventOutboxConfig while an env-loaded one is relay-on; any
// future path that constructs Config directly must set Enabled explicitly.

func (c EventOutboxConfig) withDefaults() EventOutboxConfig {
	if c.PollMilliseconds == 0 {
		c.PollMilliseconds = 1000
	}
	if c.BatchSize == 0 {
		c.BatchSize = 32
	}
	if c.ClaimTTLSeconds == 0 {
		c.ClaimTTLSeconds = 30
	}
	if c.HTTPTimeoutSeconds == 0 {
		c.HTTPTimeoutSeconds = 5
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 10
	}
	if c.DeliveredRetentionHours == 0 {
		c.DeliveredRetentionHours = 24
	}
	if c.FailedRetentionHours == 0 {
		c.FailedRetentionHours = 168
	}
	return c
}

// Validate enforces the audit-governance lease rule (ClaimTTL > 2×HTTPTimeout)
// so a slow target plus an expired lease cannot produce concurrent duplicate
// POSTs with no crash at all (concurrency-reviewer blocker, D7). The
// documented in-flight bound is targets×timeout < TTL; default TTL 30s covers
// ≤3 sequential POSTs at the 5s default timeout. Retention bounds are
// validated unconditionally — like every numeric knob, a bad value fails
// startup even when EVENT_OUTBOX_ENABLED=false (0 is rejected, not "disable").
func (c EventOutboxConfig) Validate() error {
	if c.PollMilliseconds <= 0 || c.PollMilliseconds > 60_000 {
		return errors.New("EVENT_OUTBOX_POLL_INTERVAL_MILLIS must be within 1..60000")
	}
	if c.BatchSize <= 0 || c.BatchSize > 500 {
		return errors.New("EVENT_OUTBOX_BATCH_SIZE must be within 1..500")
	}
	if c.HTTPTimeoutSeconds <= 0 || c.HTTPTimeoutSeconds > 29 {
		return errors.New("EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS must be within 1..29")
	}
	if c.ClaimTTLSeconds <= 2*c.HTTPTimeoutSeconds {
		return fmt.Errorf("EVENT_OUTBOX_CLAIM_TTL_SECONDS must exceed 2×EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS (%d×2=%d)",
			c.HTTPTimeoutSeconds, 2*c.HTTPTimeoutSeconds)
	}
	if c.ClaimTTLSeconds > 600 {
		return errors.New("EVENT_OUTBOX_CLAIM_TTL_SECONDS must not exceed 600")
	}
	if c.MaxAttempts <= 0 || c.MaxAttempts > 1000 {
		return errors.New("EVENT_OUTBOX_MAX_ATTEMPTS must be within 1..1000")
	}
	if c.DeliveredRetentionHours <= 0 || c.DeliveredRetentionHours > 8760 {
		return errors.New("EVENT_OUTBOX_DELIVERED_RETENTION_HOURS must be within 1..8760")
	}
	if c.FailedRetentionHours <= 0 || c.FailedRetentionHours > 8760 {
		return errors.New("EVENT_OUTBOX_FAILED_RETENTION_HOURS must be within 1..8760")
	}
	return nil
}
