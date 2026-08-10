package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validL2Token satisfies the ≥16-char hygiene rule.
const validL2Token = "l2-config-token-0123456789"

func validAuditSinkL2Config() AuditSinkL2Config {
	return AuditSinkL2Config{
		Endpoint: "https://audit.example.com/ingest",
		Bindings: []AuditSinkL2Binding{{Tenant: "acme", Token: validL2Token}},
	}
}

// ── H1: endpoint scheme — HTTPS or loopback HTTP only ───────────────────────

func TestAuditSinkL2Config_EndpointScheme(t *testing.T) {
	valid := validAuditSinkL2Config()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	// Empty endpoint = L2 disabled, always valid (C3).
	disabled := AuditSinkL2Config{}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled config: %v", err)
	}

	rejected := []string{
		"http://169.254.169.254/latest/meta-data", // metadata service, non-loopback HTTP
		"http://10.0.0.1/audit",
		"http://192.168.1.5/audit",
		"http://audit.example.com/ingest", // plaintext external
		"https://user:pass@example.com/ingest",
		"https://example.com/ingest?q=1",
		"https://example.com/ingest#frag",
		"ftp://example.com/ingest",
		"not-a-url",
	}
	for _, endpoint := range rejected {
		cfg := validAuditSinkL2Config()
		cfg.Endpoint = endpoint
		if err := cfg.Validate(); err == nil {
			t.Errorf("endpoint %q accepted", endpoint)
		}
	}
	accepted := []string{
		"https://audit.example.com/ingest",
		"https://example.com",
		"http://localhost:8080/ingest",
		"http://127.0.0.1:8080/ingest",
		"http://[::1]:8080/ingest",
	}
	for _, endpoint := range accepted {
		cfg := validAuditSinkL2Config()
		cfg.Endpoint = endpoint
		if err := cfg.Validate(); err != nil {
			t.Errorf("endpoint %q rejected: %v", endpoint, err)
		}
	}
}

// ── H4: bindings file discipline (0600, strict JSON, token hygiene) ─────────

func TestAuditSinkL2BindingsFile_Discipline(t *testing.T) {
	write := func(t *testing.T, body string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "bindings.json")
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatalf("write bindings: %v", err)
		}
		return path
	}
	validBody := `{"bindings":[{"tenant":"acme","token":"` + validL2Token + `"}]}`

	t.Run("valid 0600 file parses", func(t *testing.T) {
		path := write(t, validBody, 0o600)
		document, err := readAuditSinkL2Bindings(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(document.Bindings) != 1 || document.Bindings[0].Tenant != "acme" ||
			document.Bindings[0].Token != validL2Token {
			t.Fatalf("document = %+v", document)
		}
	})

	t.Run("group/other-readable file rejected", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o640, 0o604, 0o666, 0o644} {
			path := write(t, validBody, mode)
			if _, err := readAuditSinkL2Bindings(path); err == nil {
				t.Errorf("mode %04o accepted (group/other read must be rejected)", mode)
			}
		}
	})

	t.Run("unknown field rejected", func(t *testing.T) {
		path := write(t, `{"bindings":[{"tenant":"acme","token":"`+validL2Token+`","bogus":1}]}`, 0o600)
		if _, err := readAuditSinkL2Bindings(path); err == nil {
			t.Fatal("unknown field accepted")
		}
	})

	t.Run("trailing JSON rejected", func(t *testing.T) {
		path := write(t, validBody+` {"extra":true}`, 0o600)
		if _, err := readAuditSinkL2Bindings(path); err == nil {
			t.Fatal("trailing JSON accepted")
		}
	})

	t.Run("oversized file rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "big.json")
		if err := os.WriteFile(path, []byte(`{"bindings":[{"tenant":"acme","token":"`+
			strings.Repeat("x", maxAuditGovernanceBindingsBytes+1)+`"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readAuditSinkL2Bindings(path); err == nil {
			t.Fatal(">1MiB bindings accepted")
		}
	})

	t.Run("token hygiene: short/blank/duplicate rejected", func(t *testing.T) {
		cases := []string{
			`{"bindings":[{"tenant":"a","token":"short"}]}`,                                                               // < 16 chars
			`{"bindings":[{"tenant":"a","token":"  ` + validL2Token + `  "}]}`,                                            // whitespace
			`{"bindings":[{"tenant":"","token":"` + validL2Token + `"}]}`,                                                 // blank tenant
			`{"bindings":[{"tenant":"a","token":"` + validL2Token + `"},{"tenant":"a","token":"` + validL2Token + `x"}]}`, // dup tenant
			`{"bindings":[{"tenant":"a","token":"` + validL2Token + `"},{"tenant":"b","token":"` + validL2Token + `"}]}`,  // dup token
			`{"bindings":[{"tenant":"a","token_env":"AUDIT_SINK_L2_TOKEN_UNSET"}]}`,                                       // env not set
		}
		for _, body := range cases {
			path := write(t, body, 0o600)
			document, err := readAuditSinkL2Bindings(path)
			if err == nil {
				err = validateAuditSinkL2Bindings(document.Bindings)
			}
			if err == nil {
				t.Errorf("accepted: %s", body)
			}
		}
	})

	t.Run("token_env indirection resolves from environment", func(t *testing.T) {
		t.Setenv("AUDIT_SINK_L2_TOKEN_ACME", validL2Token)
		path := write(t, `{"bindings":[{"tenant":"acme","token_env":"AUDIT_SINK_L2_TOKEN_ACME"}]}`, 0o600)
		document, err := readAuditSinkL2Bindings(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if document.Bindings[0].Token != validL2Token {
			t.Fatalf("token not resolved from env: %+v", document.Bindings[0])
		}
	})

	t.Run("loadAuditSinkL2Config wires env end-to-end and fails fast on corruption", func(t *testing.T) {
		path := write(t, validBody, 0o600)
		t.Setenv("AUDIT_SINK_L2_ENDPOINT", "https://audit.example.com/ingest")
		t.Setenv("AUDIT_SINK_L2_BINDINGS_FILE", path)
		cfg, err := loadAuditSinkL2Config()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Endpoint != "https://audit.example.com/ingest" || len(cfg.Bindings) != 1 {
			t.Fatalf("cfg = %+v", cfg)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("validate: %v", err)
		}

		bad := write(t, `{"bindings":[{"tenant":"acme","token":"`+validL2Token+`","bogus":1}]}`, 0o600)
		t.Setenv("AUDIT_SINK_L2_BINDINGS_FILE", bad)
		if _, err := loadAuditSinkL2Config(); err == nil {
			t.Fatal("corrupt bindings file accepted at load (fail-fast violated)")
		}
	})
}
