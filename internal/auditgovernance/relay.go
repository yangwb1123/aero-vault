package auditgovernance

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

func (r *Runtime) reconcile() {
	for tenant := range r.bound {
		if r.states[tenant] != repository.AuditGovernanceBindingActive {
			continue
		}
		if r.stopping() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
		gaps, err := r.store.ListAuditGovernanceGaps(ctx, tenant, r.reconcileBatch)
		cancel()
		if err != nil {
			r.logger.Warn("audit governance reconcile scan failed",
				"tenant_ref", r.redactor.opaqueTenant(tenant), "error_class", "store")
			continue
		}
		for _, gap := range gaps {
			if r.stopping() {
				return
			}
			fact := r.redactor.factFromGap(gap, time.Now().UTC())
			ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
			_, err := r.store.EnqueueAuditGovernance(ctx, fact)
			cancel()
			if err != nil {
				r.logger.Warn("audit governance reconcile enqueue failed",
					"tenant_ref", r.redactor.opaqueTenant(tenant),
					"origin_ref", r.redactor.opaqueOrigin(gap), "error_class", "store")
				break
			}
		}
	}
}

func (r *Runtime) stopping() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

func (r *Runtime) deliverBatch() {
	claimToken := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	facts, err := r.store.ClaimAuditGovernance(
		ctx, r.owner, claimToken, r.revision, r.batchSize, r.claimTTL)
	cancel()
	if err != nil {
		r.logger.Warn("audit governance outbox claim failed", "error_class", "store")
		return
	}
	var workers sync.WaitGroup
	for _, fact := range facts {
		workers.Add(1)
		go func(fact repository.AuditGovernanceFact) {
			defer workers.Done()
			r.deliverFact(fact)
		}(fact)
	}
	workers.Wait()
}

func (r *Runtime) deliverFact(fact repository.AuditGovernanceFact) {
	telemetry.IncAuditGovernanceRelayAttempted(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.publisher.Publish(ctx, fact)
	cancel()
	if isPermanentDeliveryError(err) {
		// Permanent classes (contract item 1 / T-3): conflict receipt
		// (ErrReceiptConflict), invalid receipt (ErrInvalidReceipt — shape,
		// tenant mismatch, non-ledgered status) and HTTP 409/422 — all
		// terminal-with-retention. The receiver will never ledger these
		// events, so retrying is bounded-backoff-forever. Fail the fact
		// (never re-claimed) and keep the row + last_error until the
		// retention prune (7d default), mirroring the events outbox 'failed'
		// state. Transient errors fall through to retryFact. Sentinel list
		// cross-referenced by classifyRelayError below.
		r.failFact(fact, err)
		return
	}
	if err != nil {
		r.retryFact(fact, err)
		return
	}
	ctx, cancel = context.WithTimeout(context.Background(), r.httpTimeout)
	err = r.store.CompleteAuditGovernance(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken)
	cancel()
	if err != nil {
		r.logger.Warn("audit governance acknowledgement lost",
			"fact_ref", r.redactor.opaqueFact(fact), "error_class", "store")
		return // ack-lost counts nothing: the row is re-claimed and re-delivered
	}
	telemetry.IncAuditGovernanceRelayDelivered(context.Background())
}

// failFact lands a claimed fact in the terminal failed state: no retry is
// scheduled, no further POST is possible, and the row is retained with
// last_error until CleanupFailedAuditGovernance prunes it after the retention
// window (terminal-with-retention). Claim loss on the fail write is warned,
// never retried in-loop (lease re-claim is the recovery mechanism).
func (r *Runtime) failFact(fact repository.AuditGovernanceFact, cause error) {
	telemetry.IncAuditGovernanceRelayDead(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.store.FailAuditGovernance(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken, cause.Error())
	cancel()
	if err != nil {
		r.logger.Warn("audit governance terminal failure persistence failed",
			"fact_ref", r.redactor.opaqueFact(fact), "error_class", "store")
		return
	}
	r.logger.Error("audit governance fact failed terminally", "fact_ref", r.redactor.opaqueFact(fact),
		"attempt", fact.Attempts, "err", cause)
}

// cumulativeWindowExceeded is the REQ-3 terminal decision, pure and pinned
// (AC-3.2/3.4): a transient-only failure stream goes terminal once
// now - firstAttempt strictly exceeds the window — the == boundary stays
// transient (FM-10 boundary pin). Safe direction (FM-3): a zero anchor (row
// never claimed, pre-0044 row, or read-before-first-claim) is never terminal;
// a negative elapsed (DB clock ahead of relay clock at claim) is never
// terminal either (a negative duration is never > a positive window). The
// decision is monotone in now: once terminal, every later decision is
// terminal — the multi-claim-worker race invariant (a stale worker holding
// an expired lease computes the same direction as the current holder; the
// fenced fail/retry writes then land at most one outcome).
func cumulativeWindowExceeded(firstAttempt, now time.Time, window time.Duration) bool {
	return !firstAttempt.IsZero() && now.Sub(firstAttempt) > window
}

func (r *Runtime) retryFact(fact repository.AuditGovernanceFact, cause error) {
	// REQ-3: the cumulative retry window (== MaxBackoffSeconds, D3). Once
	// now - firstAttempt exceeds it, a transient-only failure stream is
	// terminal-with-retention — the same dead-row semantics as permanent
	// classes (failFact: never re-claimed, last_error retained, pruned after
	// retention). The window check precedes the failed counter so a
	// window-terminalized fact counts dead_total, never failed_total (the
	// counters stay meaningful per class). The anchor read-back is stable
	// under this worker's fence, so the decision cannot be invalidated by a
	// concurrent retry/fail write (both are fenced by owner+token+live lease).
	if cumulativeWindowExceeded(fact.FirstAttemptAt, time.Now().UTC(), r.maxBackoff) {
		r.failFact(fact, cause)
		return
	}
	telemetry.IncAuditGovernanceRelayFailed(context.Background())
	delay := boundedBackoff(fact.ID, fact.Attempts, r.initialBackoff, r.maxBackoff)
	// Package-private observation hook for the AC-2.4 e2e: captures the exact
	// scheduled delay (a pure function of fact ID + attempts) so the growth
	// assertion is independent of wall-clock scheduling noise. Never set in
	// production construction; nil-safe.
	if r.onRetry != nil {
		r.onRetry(delay)
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.store.RetryAuditGovernance(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken,
		cause.Error(), time.Now().UTC().Add(delay))
	cancel()
	if err != nil {
		r.logger.Warn("audit governance retry persistence failed",
			"fact_ref", r.redactor.opaqueFact(fact), "error_class", "store")
		return
	}
	r.logger.Warn("audit governance delivery deferred", "fact_ref", r.redactor.opaqueFact(fact),
		"attempt", fact.Attempts, "retry_in", delay, "err", classifyRelayError(cause))
}

func (r *Runtime) cleanupDelivered() {
	now := time.Now().UTC()
	if !r.nextCleanup.IsZero() && now.Before(r.nextCleanup) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	count, err := r.store.CleanupDeliveredAuditGovernance(
		ctx, now.Add(-r.retention), r.cleanupBatch)
	if err == nil {
		// Terminal-failed rows (conflict:true, contract A) share the retention
		// window: kept for diagnosis until the retention prune, then removed.
		_, err = r.store.CleanupFailedAuditGovernance(
			ctx, now.Add(-r.retention), r.cleanupBatch)
	}
	cancel()
	if err != nil {
		r.logger.Warn("audit governance delivered cleanup failed", "error_class", "store")
	}
	r.nextCleanup = now.Add(r.cleanupEvery)
	if err == nil && count == int64(r.cleanupBatch) {
		r.nextCleanup = now.Add(r.pollEvery)
	}
}

func boundedBackoff(id string, attempts int, initial, maximum time.Duration) time.Duration {
	digest := sha256.Sum256([]byte(id))
	fraction := int64(binary.BigEndian.Uint16(digest[:2]))%501 - 250
	return boundedBackoffDelay(attempts, initial, maximum, fraction)
}

// boundedBackoffDelay is the pure schedule core of boundedBackoff with the
// per-ID jitter fraction made explicit (fraction ∈ [-250, 250], i.e. ±25 %).
// Exposed only to the package tests so the strict-growth property can be
// verified exhaustively over every fraction the ID digest can produce.
func boundedBackoffDelay(attempts int, initial, maximum time.Duration, fraction int64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := initial
	for count := 1; count < attempts && delay < maximum; count++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	jittered := delay + time.Duration(int64(delay)*fraction/1000)
	return min(max(jittered, initial/2), maximum)
}

func classifyRelayError(err error) string {
	var status *httpStatusError
	if errors.As(err, &status) {
		return status.Error()
	}
	if errors.Is(err, ErrInvalidEvent) || errors.Is(err, ErrInvalidReceipt) ||
		errors.Is(err, ErrReceiptConflict) || errors.Is(err, ErrTokenUnavailable) {
		return err.Error()
	}
	return "audit governance transport failure"
}

// isPermanentDeliveryError reports whether err is one of the terminal
// delivery classes (contract item 1 / T-3): conflict receipt
// (ErrReceiptConflict), invalid receipt (ErrInvalidReceipt), or an HTTP
// 409/422 status (surfaced as *httpStatusError). Everything else — other
// HTTP statuses, ErrInvalidEvent, ErrTokenUnavailable, transport and context
// errors — is transient and retried with bounded backoff. Membership is an
// explicit closed list (wrapped sentinels classify identically via
// errors.Is/errors.As); keep in sync with classifyRelayError above.
func isPermanentDeliveryError(err error) bool {
	if errors.Is(err, ErrReceiptConflict) || errors.Is(err, ErrInvalidReceipt) {
		return true
	}
	var status *httpStatusError
	if errors.As(err, &status) {
		return status.Status == http.StatusConflict || status.Status == http.StatusUnprocessableEntity
	}
	return false
}
