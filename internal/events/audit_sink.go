package events

import (
	"context"
	"errors"
)

// ErrSinkNotBound reports that the tenant has no L2 binding configured. The
// relay treats it as a graceful degradation: the fact is completed and the
// record retained (C3) — L0 audit_log remains authoritative.
var ErrSinkNotBound = errors.New("audit sink: tenant has no L2 binding")

// ErrSinkUnauthorized reports that the L2 target rejected the credentials
// (HTTP 401/403). For a static bearer token there is no refresh path, so
// backoff retries can never succeed — the relay fails the fact immediately
// (H2, D5) instead of re-POSTing sensitive payloads at a rejecting target.
var ErrSinkUnauthorized = errors.New("audit sink: L2 rejected credentials")

// AuditSink is the L2 egress port of the deletion audit path (FR-2). L0
// (local audit_log, written in the delete transaction) is always on; the L2
// adapter is opt-in and injected into the relay through
// EventOutboxRelayOptions. Core code never imports the adapter directly.
//
// Contract (C9–C11, at-least-once): DeliverDeleted delivers the
// vault.file.deleted@1.1 envelope as fact.Payload's verbatim bytes to the
// tenant-bound L2 target; factID (the outbox row id) travels in the
// X-Audit-Fact-Id request header so the receiver can dedupe (echo receipt,
// D5). Returning nil means the target acknowledged with 2xx AND echoed the
// fact id — the receiver commit point. Errors:
//
//   - ErrSinkNotBound: no binding for the tenant → relay completes (record
//     retained, L0 authoritative).
//   - ErrSinkUnauthorized: 401/403 → relay fails the fact immediately.
//   - any other error (including a 2xx without the echo) → relay retries
//     with the existing backoff+jitter until maxAttempts → terminal failed.
type AuditSink interface {
	DeliverDeleted(ctx context.Context, tenant string, factID int64, payload []byte) error
}
