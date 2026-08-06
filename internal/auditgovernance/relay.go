package auditgovernance

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/repository"
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
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.publisher.Publish(ctx, fact)
	cancel()
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
	}
}

func (r *Runtime) retryFact(fact repository.AuditGovernanceFact, cause error) {
	delay := boundedBackoff(fact.ID, fact.Attempts, r.initialBackoff, r.maxBackoff)
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
	digest := sha256.Sum256([]byte(id))
	fraction := int64(binary.BigEndian.Uint16(digest[:2]))%501 - 250
	jittered := delay + time.Duration(int64(delay)*fraction/1000)
	return min(max(jittered, initial/2), maximum)
}

func classifyRelayError(err error) string {
	var status *httpStatusError
	if errors.As(err, &status) {
		return status.Error()
	}
	if errors.Is(err, ErrInvalidEvent) || errors.Is(err, ErrInvalidReceipt) ||
		errors.Is(err, ErrTokenUnavailable) {
		return err.Error()
	}
	return "audit governance transport failure"
}
