// Package auditgovernance relays redacted Aero Vault facts to the separately
// deployed Snaplink Audit Governance service.
package auditgovernance

import (
	"errors"
	"fmt"
	"time"
)

const (
	SourcePrefix     = "aero-vault"
	SchemaID         = "aero.vault.security"
	SchemaVersion    = 1
	Classification   = "confidential"
	RetentionClass   = "security"
	RequiredScope    = "audit:event:write"
	RequiredResource = "audit-governance"
	governancePath   = "api/v1/events"
	maxResponseBytes = 64 << 10
)

var (
	ErrInvalidConfig    = errors.New("audit governance configuration is invalid")
	ErrInvalidEvent     = errors.New("audit governance event is invalid")
	ErrInvalidReceipt   = errors.New("audit governance receipt is invalid")
	ErrReceiptConflict  = errors.New("audit governance receipt reports a conflict")
	ErrTokenUnavailable = errors.New("audit governance token is unavailable")
)

type httpStatusError struct {
	Status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("audit governance HTTP %d", e.Status)
}

type receiptEnvelope struct {
	Receipt struct {
		EventID    string    `json:"event_id"`
		TenantID   string    `json:"tenant_id"`
		Status     string    `json:"status"`
		AcceptedAt time.Time `json:"accepted_at"`
		// Duplicate is deliberately unused in the acceptance predicate
		// (contract A): an idempotent re-POST — lease re-claim, crash
		// re-delivery, at-least-once — must be answered
		// {duplicate:true, conflict:false, status:ledgered} and accepted
		// exactly like a first POST. Never gate acceptance on Duplicate;
		// the contract test TestReceiptDuplicateSemanticsContract pins this.
		Duplicate bool `json:"duplicate"`
		// Conflict means the receiver will never ledger this event (semantic
		// collision, not idempotency). Terminal-with-retention: the relay
		// fails the fact (ErrReceiptConflict), never retries it, and keeps
		// the row until the retention prune.
		Conflict bool `json:"conflict"`
	} `json:"receipt"`
}

type governanceEvent struct {
	EventID            string             `json:"event_id"`
	SourceSystem       string             `json:"source_system"`
	EventType          string             `json:"event_type"`
	SchemaID           string             `json:"schema_id"`
	SchemaVersion      int                `json:"schema_version"`
	OccurredAt         time.Time          `json:"occurred_at"`
	OperationID        string             `json:"operation_id,omitempty"`
	Actor              governanceActor    `json:"actor"`
	Targets            []governanceTarget `json:"targets,omitempty"`
	AggregateType      string             `json:"aggregate_type,omitempty"`
	AggregateID        string             `json:"aggregate_id,omitempty"`
	Action             string             `json:"action"`
	Outcome            string             `json:"outcome"`
	Payload            map[string]any     `json:"payload"`
	DataClassification string             `json:"data_classification"`
	RetentionClass     string             `json:"retention_class"`
	IdempotencyKey     string             `json:"idempotency_key"`
}

type governanceActor struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type governanceTarget struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
