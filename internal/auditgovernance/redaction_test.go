package auditgovernance

import (
	"strings"
	"testing"
)

func TestRedactionIsKeyedTenantScopedAndDomainSeparated(t *testing.T) {
	redactor, err := newRedactor("audit-governance-hmac-key-32-bytes-minimum")
	if err != nil {
		t.Fatal(err)
	}
	first := redactor.digest("tenant-a", "actor", "admin")
	if first != redactor.digest("tenant-a", "actor", "admin") {
		t.Fatal("HMAC redaction is not deterministic")
	}
	for _, other := range []string{
		redactor.digest("tenant-b", "actor", "admin"),
		redactor.digest("tenant-a", "target", "admin"),
	} {
		if other == first {
			t.Fatal("HMAC redaction crossed a tenant or field domain")
		}
	}
	rotated, _ := newRedactor("rotated-audit-governance-hmac-key-material")
	if rotated.digest("tenant-a", "actor", "admin") == first {
		t.Fatal("HMAC key rotation did not change new digests")
	}
	if strings.Contains(first, "tenant-a") || strings.Contains(first, "admin") {
		t.Fatalf("digest leaked raw input: %q", first)
	}
}
