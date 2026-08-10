package auditgovernance

import (
	"strconv"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func (r *redactor) factFromAudit(
	entry repository.AuditEntry, now time.Time,
) repository.AuditGovernanceFact {
	tenant := normalizedTenant(entry.TenantID)
	source, _ := r.tenantSourceID(tenant) // normalizedTenant (non-empty, trimmed) makes the error unreachable
	occurred, err := time.Parse(time.RFC3339Nano, entry.CreatedAt)
	if err != nil || occurred.IsZero() {
		occurred = now.UTC()
	}
	return repository.AuditGovernanceFact{
		SourceID: source, TenantID: tenant, OriginKind: repository.AuditOriginAdmin,
		FactKind: auditFactKind(entry.Action), ActorDigest: r.digest(tenant, "actor", entry.Actor),
		Action:       safeAction(entry.Action, "admin.unknown"),
		TargetDigest: r.digest(tenant, "target", entry.Target),
		DetailSHA256: r.digest(tenant, "detail", entry.Detail), OccurredAt: occurred,
	}
}

func (r *redactor) factFromEvent(
	event repository.Event, now time.Time,
) repository.AuditGovernanceFact {
	tenant := normalizedTenant(event.TenantID)
	source, _ := r.tenantSourceID(tenant) // normalizedTenant (non-empty, trimmed) makes the error unreachable
	occurred := event.CreatedAt.UTC()
	if occurred.IsZero() {
		occurred = now.UTC()
	}
	return repository.AuditGovernanceFact{
		SourceID: source, TenantID: tenant, OriginKind: repository.AuditOriginFile,
		FactKind: "file", Action: safeAction("file."+string(event.Type), "file.unknown"),
		TargetDigest:    r.digest(tenant, "file-target", event.Bucket+"\x00"+event.Key),
		RequestID:       r.digest(tenant, "request", event.RequestID),
		ObjectSizeBytes: payloadSize(event.Payload),
		StorageBackend:  safeBackend(event.Payload["backend"]), OccurredAt: occurred,
	}
}

func (r *redactor) factFromGap(
	gap repository.AuditGovernanceGap, now time.Time,
) repository.AuditGovernanceFact {
	var fact repository.AuditGovernanceFact
	if gap.OriginKind == repository.AuditOriginFile {
		event := repository.Event{TenantID: gap.TenantID, Bucket: gap.Bucket, Key: gap.Key,
			Type:      repository.EventType(strings.TrimPrefix(gap.Action, "file.")),
			RequestID: gap.RequestID, Payload: gap.Payload, CreatedAt: gap.OccurredAt}
		fact = r.factFromEvent(event, now)
	} else {
		createdAt := ""
		if !gap.OccurredAt.IsZero() {
			createdAt = gap.OccurredAt.Format(time.RFC3339Nano)
		}
		entry := repository.AuditEntry{TenantID: gap.TenantID, Actor: gap.Actor, Action: gap.Action,
			Target: gap.Target, Detail: gap.Detail, CreatedAt: createdAt}
		fact = r.factFromAudit(entry, now)
	}
	fact.OriginID = gap.OriginID
	// Single call site for the formula: same inputs as the atomic path
	// (REQ-2 canonicalized occurred), so capture and gap reconcile converge
	// on the same ID (B3.3 / T-4).
	fact.ID = repository.DeterministicFactID(fact.SourceID, fact.TenantID,
		fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)
	return fact
}

func normalizedTenant(tenant string) string {
	if tenant == "" {
		return "default"
	}
	return tenant
}

func auditFactKind(action string) string {
	if strings.HasPrefix(action, "key.") || action == "tenant.status" || action == "tenant.delete" {
		return "security"
	}
	return "admin"
}

func safeAction(action, fallback string) string {
	if action == "" || len(action) > 128 {
		return fallback
	}
	for _, char := range action {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._:-", char) {
			continue
		}
		return fallback
	}
	return action
}

func payloadSize(payload map[string]string) int64 {
	value, err := strconv.ParseInt(payload["size"], 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func safeBackend(value string) string {
	switch value {
	case "local", "s3", "oss", "cos":
		return value
	default:
		return ""
	}
}
