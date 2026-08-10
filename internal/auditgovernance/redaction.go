package auditgovernance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"hash"
	"sort"
	"strconv"
	"strings"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

const redactionDomain = "aero-vault/audit-governance/v1"

type redactor struct {
	key []byte
}

func newRedactor(key string) (*redactor, error) {
	if len(key) < 32 || len(key) > 4096 {
		return nil, ErrInvalidConfig
	}
	return &redactor{key: append([]byte(nil), key...)}, nil
}

func (r *redactor) digest(tenant, field, value string) string {
	if value == "" {
		return ""
	}
	mac := hmac.New(sha256.New, r.key)
	writeMACFields(mac, redactionDomain, tenant, field, value)
	return "hmac-sha256:" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (r *redactor) opaqueTenant(tenant string) string {
	return "opaque:" + strings.TrimPrefix(
		r.digest(tenant, "log-tenant", tenant), "hmac-sha256:")
}

func (r *redactor) tenantSourceID(tenant string) (string, error) {
	if tenant == "" || tenant != strings.TrimSpace(tenant) {
		return "", ErrInvalidConfig
	}
	// The fact-ID frame (audit_governance_factid.go) is NUL-separated and
	// unambiguous only if no field carries a control character. tenant is the
	// only unconstrained frame field — reject C0 controls (incl. NUL) + DEL
	// so the framing's injectivity never depends on the tenant input.
	if strings.IndexFunc(tenant, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", ErrInvalidConfig
	}
	value := r.digest(tenant, "source-system", tenant)
	return SourcePrefix + "." + strings.TrimPrefix(value, "hmac-sha256:"), nil
}

func (r *redactor) opaqueFact(fact repository.AuditGovernanceFact) string {
	return "opaque:" + strings.TrimPrefix(
		r.digest(fact.TenantID, "log-fact", fact.ID), "hmac-sha256:")
}

func (r *redactor) opaqueOrigin(gap repository.AuditGovernanceGap) string {
	value := gap.OriginKind + ":" + strconvFormatInt(gap.OriginID)
	return "opaque:" + strings.TrimPrefix(
		r.digest(gap.TenantID, "log-origin", value), "hmac-sha256:")
}

func (r *redactor) desiredDigest(cfg config.AuditGovernanceConfig) string {
	bindings := append([]config.AuditGovernanceBinding(nil), cfg.Bindings...)
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].TenantID < bindings[j].TenantID })
	mac := hmac.New(sha256.New, r.key)
	_, _ = mac.Write([]byte(redactionDomain + "\x00binding-manifest\x00"))
	for _, binding := range bindings {
		writeMACFields(mac, binding.TenantID, binding.State, binding.ClientID,
			binding.ClientSecretEnv, binding.ClientSecret)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func writeMACFields(mac hash.Hash, fields ...string) {
	for _, field := range fields {
		_, _ = mac.Write([]byte(field))
		_, _ = mac.Write([]byte{0})
	}
}

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
