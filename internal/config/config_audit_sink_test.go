package config

import "testing"

const auditSinkTestToken = "audit-sink-test-token-012345"

func TestAuditSinkKindDerivation(t *testing.T) {
	legacy := validAuditGovernanceConfig()
	bearer := AuditSinkL2Config{
		Endpoint: "https://audit.example.test",
		Bindings: []AuditSinkL2Binding{{Tenant: "acme", Token: auditSinkTestToken}},
	}
	cases := []struct {
		name   string
		kind   string
		legacy AuditGovernanceConfig
		bearer AuditSinkL2Config
		want   string
	}{
		{name: "default l0"},
		{name: "explicit l1", kind: "l1", want: AuditSinkKindL1},
		{name: "legacy derives l2", legacy: legacy, want: AuditSinkKindL2},
		{name: "bearer derives l2", bearer: bearer, want: AuditSinkKindL2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AUDIT_SINK_KIND", tc.kind)
			got, err := loadAuditSinkConfig(tc.legacy, tc.bearer)
			if err != nil {
				t.Fatalf("loadAuditSinkConfig: %v", err)
			}
			want := tc.want
			if want == "" {
				want = AuditSinkKindL0
			}
			if got.Kind != want {
				t.Fatalf("kind = %q, want %q", got.Kind, want)
			}
		})
	}
}

func TestAuditSinkKindRejectsAmbiguousOrInvalidConfig(t *testing.T) {
	legacy := validAuditGovernanceConfig()
	bearer := AuditSinkL2Config{
		Endpoint: "https://audit.example.test",
		Bindings: []AuditSinkL2Binding{{Tenant: "acme", Token: auditSinkTestToken}},
	}
	cases := []struct {
		name   string
		kind   string
		legacy AuditGovernanceConfig
		bearer AuditSinkL2Config
	}{
		{name: "unknown kind", kind: "L3"},
		{name: "l0 with legacy", kind: "L0", legacy: legacy},
		{name: "l1 with bearer", kind: "L1", bearer: bearer},
		{name: "l2 with both", kind: "L2", legacy: legacy, bearer: bearer},
		{name: "l2 without endpoint", kind: "L2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AUDIT_SINK_KIND", tc.kind)
			if _, err := loadAuditSinkConfig(tc.legacy, tc.bearer); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestAuditSinkConfigValidateZeroAndL1(t *testing.T) {
	for _, cfg := range []AuditSinkConfig{
		{},
		{Kind: "l0"},
		{Kind: AuditSinkKindL1},
	} {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("AuditSinkConfig(%+v).Validate() = %v", cfg, err)
		}
	}
}

func TestAuditSinkConfigValidateRejectsL2BindingsForL0(t *testing.T) {
	cfg := AuditSinkConfig{
		Kind:     AuditSinkKindL0,
		Bindings: []AuditSinkL2Binding{{Tenant: "acme", Token: auditSinkTestToken}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("AuditSinkConfig accepted L2 bindings for L0")
	}
}

func TestAuditSinkBearerCredentialsStaySeparateFromBilling(t *testing.T) {
	billing := BillingConfig{Enabled: true, Bindings: []BillingBinding{{ClientSecret: "shared-secret"}}}
	sink := AuditSinkConfig{
		Kind:     AuditSinkKindL2,
		Endpoint: "https://audit.example.test",
		Bindings: []AuditSinkL2Binding{{Tenant: "acme", Token: "shared-secret"}},
	}
	if err := validateCommercialCredentialSeparation(billing, sink); err == nil {
		t.Fatal("bearer token reused a billing credential")
	}
	bearer, legacy := sink.L2Variant()
	if !bearer || legacy {
		t.Fatalf("L2Variant() = bearer=%v legacy=%v, want true/false", bearer, legacy)
	}
}
