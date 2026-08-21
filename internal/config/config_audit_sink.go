package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	AuditSinkKindL0 = "L0"
	AuditSinkKindL1 = "L1"
	AuditSinkKindL2 = "L2"
)

// AuditSinkConfig is the configuration-level adapter selector. L0 is the
// local audit log, L1 is the existing protocol/event surface, and L2 is an
// external governance sink. The legacy OAuth/HMAC block remains embedded for
// compatibility; the bearer L2 fields mirror AuditSinkL2Config.
type AuditSinkConfig struct {
	Kind         string
	Endpoint     string
	BindingsFile string
	Bindings     []AuditSinkL2Binding
	Legacy       AuditGovernanceConfig
}

func loadAuditSinkConfig(legacy AuditGovernanceConfig, bearer AuditSinkL2Config) (AuditSinkConfig, error) {
	raw, _ := os.LookupEnv("AUDIT_SINK_KIND")
	raw = strings.TrimSpace(raw)
	kind := strings.ToUpper(raw)
	bearerConfigured := bearer.Endpoint != "" || bearer.BindingsFile != "" || len(bearer.Bindings) > 0
	if kind == "" {
		switch {
		case legacy.Enabled:
			kind = AuditSinkKindL2
		case bearerConfigured:
			kind = AuditSinkKindL2
		default:
			kind = AuditSinkKindL0
		}
	}
	cfg := AuditSinkConfig{
		Kind:         kind,
		Endpoint:     bearer.Endpoint,
		BindingsFile: bearer.BindingsFile,
		Bindings:     bearer.Bindings,
		Legacy:       legacy,
	}
	if err := cfg.validateSelection(bearerConfigured); err != nil {
		return AuditSinkConfig{}, err
	}
	return cfg, nil
}

func (c AuditSinkConfig) validateSelection(bearerConfigured bool) error {
	kind := normalizeAuditSinkKind(c.Kind)
	switch kind {
	case AuditSinkKindL0, AuditSinkKindL1:
		if c.Legacy.Enabled || bearerConfigured || c.Endpoint != "" || c.BindingsFile != "" {
			return fmt.Errorf("AUDIT_SINK_KIND=%s cannot carry L2 credentials", kind)
		}
		return nil
	case AuditSinkKindL2:
		if c.Legacy.Enabled && (c.Endpoint != "" || c.BindingsFile != "" || len(c.Bindings) > 0) {
			return fmt.Errorf("AUDIT_SINK_KIND=L2 has conflicting legacy and bearer credentials")
		}
		if c.Legacy.Enabled {
			return c.Legacy.Validate()
		}
		if c.Endpoint == "" {
			return fmt.Errorf("AUDIT_SINK_L2_ENDPOINT is required when AUDIT_SINK_KIND=L2")
		}
		return (AuditSinkL2Config{
			Endpoint: c.Endpoint, BindingsFile: c.BindingsFile, Bindings: c.Bindings,
		}).Validate()
	default:
		return fmt.Errorf("AUDIT_SINK_KIND must be L0, L1, or L2 (got %q)", c.Kind)
	}
}

// Validate checks a directly constructed selector. An empty Kind is the
// zero-value compatibility form and means L0; Load always materializes it.
func (c AuditSinkConfig) Validate() error {
	return c.validateSelection(c.Endpoint != "" || c.BindingsFile != "" || len(c.Bindings) > 0)
}

// L2Variant reports which mutually exclusive L2 adapter a validated selector
// chose. Invalid or non-L2 selectors return (false, false); callers that need
// startup enforcement must call Validate first.
func (c AuditSinkConfig) L2Variant() (bearer, legacy bool) {
	if normalizeAuditSinkKind(c.Kind) != AuditSinkKindL2 {
		return false, false
	}
	if c.Legacy.Enabled {
		return false, true
	}
	return c.Endpoint != "" || c.BindingsFile != "" || len(c.Bindings) > 0, false
}

func normalizeAuditSinkKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return AuditSinkKindL0
	}
	return strings.ToUpper(strings.TrimSpace(kind))
}
