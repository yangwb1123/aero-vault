package billing

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

func (r *Runtime) runOutbox(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.deliverBatch(ctx)
		r.probeAndRecord(ctx)
		timer.Reset(r.pollEvery)
	}
}

func (r *Runtime) deliverBatch(ctx context.Context) {
	facts, err := r.store.ClaimBillingUsage(ctx, r.owner, r.batchSize, r.claimTTL)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("billing usage claim failed", "err", err)
		}
		return
	}
	var workers sync.WaitGroup
	for _, fact := range facts {
		if ctx.Err() != nil {
			break
		}
		workers.Add(1)
		go func(fact repository.BillingUsageFact) {
			defer workers.Done()
			r.deliverFact(ctx, fact)
		}(fact)
	}
	workers.Wait()
}

func (r *Runtime) deliverFact(ctx context.Context, fact repository.BillingUsageFact) {
	telemetry.IncBillingRelayAttempted(ctx)
	client, ok := r.bindings[fact.TenantID]
	if !ok {
		r.failFact(ctx, fact, errors.New("billing tenant binding missing"))
		return
	}
	metadata := map[string]string{}
	if err := json.Unmarshal([]byte(fact.MetadataJSON), &metadata); err != nil {
		r.failFact(ctx, fact, errors.New("billing usage metadata invalid"))
		return
	}
	err := client.AppendUsage(ctx, fact.ID, fact.Dimension, fact.Quantity, fact.OccurredAt, metadata)
	if err != nil {
		if isPermanentBillingError(err) {
			r.failFact(ctx, fact, err)
		} else {
			r.retryFact(ctx, fact, err)
		}
		return
	}
	if err := r.store.CompleteBillingUsage(ctx, fact.ID, fact.ClaimOwner); err != nil {
		r.logger.Warn("billing usage acknowledgement failed", "fact_id", fact.ID, "err", err)
		return
	}
	telemetry.IncBillingRelayDelivered(ctx)
}

func (r *Runtime) failFact(ctx context.Context, fact repository.BillingUsageFact, cause error) {
	if err := r.store.RetryBillingUsage(ctx, fact.ID, fact.ClaimOwner, cause.Error(), time.Now().UTC(), fact.Attempts); err != nil {
		r.logger.Warn("billing usage terminal failure persistence failed", "fact_id", fact.ID, "err", err)
		return
	}
	telemetry.IncBillingRelayDead(ctx)
	r.logger.Error("billing usage fact rejected permanently", "fact_id", fact.ID, "attempt", fact.Attempts, "err", cause)
}

func (r *Runtime) retryFact(ctx context.Context, fact repository.BillingUsageFact, cause error) {
	delay := billingBackoff(fact.Attempts)
	next := time.Now().UTC().Add(delay)
	if err := r.store.RetryBillingUsage(ctx, fact.ID, fact.ClaimOwner, cause.Error(), next, r.maxAttempts); err != nil {
		r.logger.Warn("billing usage retry persistence failed", "fact_id", fact.ID, "err", err)
		return
	}
	if fact.Attempts >= maxAttemptsOrOne(r.maxAttempts) {
		telemetry.IncBillingRelayDead(ctx)
	} else {
		telemetry.IncBillingRelayFailed(ctx)
	}
	r.logger.Warn("billing usage delivery deferred", "fact_id", fact.ID,
		"attempt", fact.Attempts, "retry_in", delay, "err", cause)
}

func isPermanentBillingError(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500
}

func maxAttemptsOrOne(maxAttempts int) int {
	if maxAttempts < 1 {
		return 1
	}
	return maxAttempts
}

func billingBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second
	for i := 1; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	jitter := time.Duration(rand.Int63n(int64(delay)/2+1)) - delay/4
	return delay + jitter
}
