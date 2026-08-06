package billing

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
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
	client, ok := r.bindings[fact.TenantID]
	if !ok {
		r.retryFact(ctx, fact, errors.New("billing tenant binding missing"))
		return
	}
	metadata := map[string]string{}
	if err := json.Unmarshal([]byte(fact.MetadataJSON), &metadata); err != nil {
		r.retryFact(ctx, fact, errors.New("billing usage metadata invalid"))
		return
	}
	err := client.AppendUsage(ctx, fact.ID, fact.Dimension, fact.Quantity, fact.OccurredAt, metadata)
	if err != nil {
		r.retryFact(ctx, fact, err)
		return
	}
	if err := r.store.CompleteBillingUsage(ctx, fact.ID, fact.ClaimOwner); err != nil {
		r.logger.Warn("billing usage acknowledgement failed", "fact_id", fact.ID, "err", err)
	}
}

func (r *Runtime) retryFact(ctx context.Context, fact repository.BillingUsageFact, cause error) {
	delay := billingBackoff(fact.Attempts)
	next := time.Now().UTC().Add(delay)
	if err := r.store.RetryBillingUsage(ctx, fact.ID, fact.ClaimOwner, cause.Error(), next); err != nil {
		r.logger.Warn("billing usage retry persistence failed", "fact_id", fact.ID, "err", err)
		return
	}
	r.logger.Warn("billing usage delivery deferred", "fact_id", fact.ID,
		"attempt", fact.Attempts, "retry_in", delay, "err", cause)
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
