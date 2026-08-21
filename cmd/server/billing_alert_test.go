package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/config"
)

func TestAlertsYMLBillingExprParity(t *testing.T) {
	t.Setenv("BILLING_ENABLED", "false")
	t.Setenv("BILLING_MAX_LAG_SECONDS", "")
	t.Setenv("AUDIT_GOVERNANCE_ENABLED", "false")
	t.Setenv("AUDIT_SINK_KIND", "L0")
	cfg, err := loadTestConfig()
	if err != nil {
		t.Fatal(err)
	}
	wantExpr := "expr: billing_backlog_age_seconds > " +
		strconv.Itoa(cfg.Billing.MaxLagSeconds/2)
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "prometheus", "alerts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if got := strings.Count(text, "expr: billing_"); got != 1 {
		t.Fatalf("alerts.yml has %d billing expressions, want 1", got)
	}
	marker := "alert: BillingBacklogDegraded"
	idx := strings.Index(text, marker)
	if idx < 0 {
		t.Fatalf("alerts.yml missing %q", marker)
	}
	block := text[idx:]
	for _, want := range []string{wantExpr, "for: 10m", "severity: warning", "/readyz stays 200"} {
		if !strings.Contains(block, want) {
			t.Fatalf("billing alert block missing %q", want)
		}
	}
}

func loadTestConfig() (*config.Config, error) {
	return config.Load()
}
