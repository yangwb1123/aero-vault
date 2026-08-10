package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// EventOutboxRelay drains the transactional deletion outbox: claim → deliver →
// complete, with exponential backoff retry and DELETE-style pruning. Claim
// fencing (owner+token+lease) follows the audit governance shape; the claim
// predicate and status model follow billing (D5/D7).
//
// Semantics (explicit, D7):
//   - exactly-once holds only AFTER complete; the deliver→complete window is
//     at-least-once (a crashed/lease-expired relay re-claims and may re-POST —
//     S3-equivalent; receivers must be idempotent).
//   - claim-lost on complete/retry is warned + counted, never retried in-loop
//     (lease re-claim is the recovery mechanism; treating it as a delivery
//     failure would double-schedule).
//   - no cross-fact ordering guarantee (retries push available_at_ns forward;
//     multi-instance relays interleave).
type EventOutboxRelay struct {
	repo        repository.Repository
	logger      *slog.Logger
	client      *http.Client
	sink        AuditSink
	owner       string
	pollEvery   time.Duration
	batchSize   int
	claimTTL    time.Duration
	httpTimeout time.Duration
	maxAttempts int

	deliveredRetain time.Duration // prune horizon for delivered rows (default 24h)
	failedRetain    time.Duration // prune horizon for terminal-failed rows (default 7d)
}

// EventOutboxRelayOptions configures the relay. Zero values fall back to the
// billing-mirrored defaults (poll 1000ms / batch 32 / TTL 30s / delivered
// retain 24h / failed retain 7d). AuditSink, when non-nil, receives
// deleted@1.1 facts (L2 egress); nil keeps the complete-only behavior (L0
// audit_log remains authoritative).
type EventOutboxRelayOptions struct {
	PollInterval    time.Duration
	BatchSize       int
	ClaimTTL        time.Duration
	HTTPTimeout     time.Duration
	MaxAttempts     int
	AuditSink       AuditSink
	DeliveredRetain time.Duration // ≤ 0 → eventOutboxDeliveredRetain (24h)
	FailedRetain    time.Duration // ≤ 0 → eventOutboxFailedRetain (7×24h)
}

const (
	eventOutboxPruneEveryRounds = 60 // ≈1 min at the default 1000ms poll
	eventOutboxDeliveredRetain  = 24 * time.Hour
	eventOutboxFailedRetain     = 7 * 24 * time.Hour
	eventOutboxMaxBackoff       = 5 * time.Minute
	eventOutboxRemoveEventName  = "s3:ObjectRemoved:Delete"
)

// NewEventOutboxRelay builds a relay. Started by cmd/server unless
// EVENT_OUTBOX_ENABLED=false — the core deletion atomicity is never gated;
// with no notification rules the relay is a silent no-op.
func NewEventOutboxRelay(
	repo repository.Repository, logger *slog.Logger, opts EventOutboxRelayOptions,
) *EventOutboxRelay {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 1000 * time.Millisecond
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 32
	}
	if opts.ClaimTTL <= 0 {
		opts.ClaimTTL = 30 * time.Second
	}
	if opts.HTTPTimeout <= 0 {
		opts.HTTPTimeout = 5 * time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 10
	}
	if opts.DeliveredRetain <= 0 {
		opts.DeliveredRetain = eventOutboxDeliveredRetain
	}
	if opts.FailedRetain <= 0 {
		opts.FailedRetain = eventOutboxFailedRetain
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return &EventOutboxRelay{
		repo:        repo,
		logger:      logger,
		client:      &http.Client{Timeout: opts.HTTPTimeout},
		sink:        opts.AuditSink,
		owner:       fmt.Sprintf("event-outbox:%s", hostname),
		pollEvery:   opts.PollInterval,
		batchSize:   opts.BatchSize,
		claimTTL:    opts.ClaimTTL,
		httpTimeout: opts.HTTPTimeout,
		maxAttempts: opts.MaxAttempts,

		deliveredRetain: opts.DeliveredRetain,
		failedRetain:    opts.FailedRetain,
	}
}

// Run polls the outbox until ctx is cancelled.
func (r *EventOutboxRelay) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	rounds := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.deliverBatch(ctx)
		rounds++
		if rounds%eventOutboxPruneEveryRounds == 0 {
			r.prune()
		}
		timer.Reset(r.pollEvery)
	}
}

// newClaimToken returns a per-batch fencing token: crypto/rand 16 bytes → 32
// hex chars (audit relay per-batch token precedent, stdlib only).
func newClaimToken() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString(buf[:])
	}
	return hex.EncodeToString(buf[:])
}

func (r *EventOutboxRelay) deliverBatch(ctx context.Context) {
	token := newClaimToken()
	// Claim under a bounded context; keep the caller ctx uncancelled for the
	// per-fact dispatch below (a canceled ctx would abort the delivery loop).
	claimCtx, cancel := context.WithTimeout(ctx, r.httpTimeout)
	facts, err := r.repo.ClaimEventOutbox(claimCtx, r.owner, token, r.batchSize, r.claimTTL)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("event outbox claim failed", "err", err)
		}
		return
	}
	var workers sync.WaitGroup
	for _, fact := range facts {
		if ctx.Err() != nil {
			break
		}
		workers.Add(1)
		go func(fact repository.EventOutboxRow) {
			defer workers.Done()
			r.deliverFact(fact)
		}(fact)
	}
	workers.Wait()
}

// deliverFact dispatches one claimed fact by type. deleted@1.1 is a durable
// lifecycle record with no local re-broadcast (D3 — re-broadcasting would
// double-fire webhook/indexer/AV/replication/SSE, N3); with an AuditSink
// bound it is delivered to L2 (FR-4), otherwise it completes + telemetry.
func (r *EventOutboxRelay) deliverFact(fact repository.EventOutboxRow) {
	switch fact.EventType {
	case repository.EventTypeFileDeleted11:
		r.deliverDeleted(fact)
	case repository.EventTypeFileNotify11:
		r.deliverNotify(fact)
	default:
		r.retry(fact, fmt.Errorf("unknown event outbox fact type %q", fact.EventType))
	}
}

// deliverDeleted routes one deleted@1.1 fact to the AuditSink (D5):
//   - sink == nil (L2 not configured) → complete, record retained (C3)
//   - ErrSinkNotBound → complete + l2_unbound_total (per-tenant opt-in)
//   - ErrSinkUnauthorized (401/403) → terminal failed immediately, no
//     backoff: claim already consumed the attempts budget, so retrying with
//     maxAttempts=attempts lands the fact in 'failed' (H2).
//   - other errors (5xx/transport/echo mismatch) → existing backoff+jitter
//     until maxAttempts → failed (F1).
func (r *EventOutboxRelay) deliverDeleted(fact repository.EventOutboxRow) {
	if r.sink == nil {
		r.complete(fact)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.sink.DeliverDeleted(ctx, fact.TenantID, fact.ID, fact.Payload)
	cancel()
	switch {
	case err == nil:
		telemetry.IncEventOutboxL2Delivered(context.Background())
		r.complete(fact)
	case errors.Is(err, ErrSinkNotBound):
		telemetry.IncEventOutboxL2Unbound(context.Background())
		r.complete(fact)
	case errors.Is(err, ErrSinkUnauthorized):
		r.failImmediately(fact)
	default:
		r.retry(fact, err)
	}
}

// failImmediately lands a claimed fact in the terminal 'failed' state
// without backoff (H2). Claim already incremented attempts, so passing
// maxAttempts=fact.Attempts makes RetryEventOutbox's terminal predicate
// (attempts >= maxAttempts) hold on the first write.
func (r *EventOutboxRelay) failImmediately(fact repository.EventOutboxRow) {
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.repo.RetryEventOutbox(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken,
		ErrSinkUnauthorized.Error(), time.Now().UTC(), fact.Attempts)
	cancel()
	if err != nil {
		r.logger.Warn("event outbox L2 rejection persistence failed", "fact", fact.ID, "err", err)
		telemetry.IncEventOutboxClaimLost(context.Background())
		return
	}
	telemetry.IncEventOutboxL2Rejected(context.Background())
	telemetry.IncEventOutboxFailed(context.Background())
	r.logger.Warn("event outbox L2 rejected credentials; fact failed",
		"fact", fact.ID, "tenant", fact.TenantID, "attempts", fact.Attempts)
}

// deliverNotify posts the stored payload byte-exact to every matching target
// (rules resolved at delivery time; the payload is never re-derived — E7
// fix). Any target failure retries the whole fact; no rules/matches completes
// it (the fact is still consumed; rules may be configured later).
func (r *EventOutboxRelay) deliverNotify(fact repository.EventOutboxRow) {
	meta, err := parseNotifyPayload(fact.Payload)
	if err != nil {
		r.retry(fact, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	rules, err := r.repo.GetBucketNotifications(ctx, meta.Tenant, meta.Bucket)
	cancel()
	if err != nil {
		r.retry(fact, err)
		return
	}
	start := time.Now()
	deliveryCtx, cancel := context.WithTimeout(context.Background(), r.claimTTL)
	err = deliverPayload(deliveryCtx, r.client, rules, eventOutboxRemoveEventName, meta.Key, fact.Payload)
	cancel()
	if elapsed := time.Since(start); elapsed > r.claimTTL/2 {
		r.logger.Warn("event outbox delivery approached its lease; raise EVENT_OUTBOX_CLAIM_TTL_SECONDS above targets×timeout",
			"fact", fact.ID, "elapsed", elapsed, "lease", r.claimTTL)
	}
	if err != nil {
		r.retry(fact, err)
		return
	}
	r.complete(fact)
}

// notifyPayloadMeta is the fixed subset of the notify@1.1 schema the relay
// needs to re-resolve rules at delivery time.
type notifyPayloadMeta struct {
	Tenant string `json:"tenant"`
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
}

func parseNotifyPayload(payload []byte) (notifyPayloadMeta, error) {
	var meta notifyPayloadMeta
	if err := json.Unmarshal(payload, &meta); err != nil {
		return meta, fmt.Errorf("event outbox payload invalid: %w", err)
	}
	if meta.Tenant == "" || meta.Bucket == "" {
		return meta, errors.New("event outbox payload missing tenant or bucket")
	}
	return meta, nil
}

// deliverPayload POSTs body to every target of every rule matching the event
// name and key, sequentially (billing deliverBatch shape). Returns the first
// error so callers can decide retry semantics.
func deliverPayload(
	ctx context.Context, client *http.Client, rules []repository.NotificationRule,
	eventName, key string, body []byte,
) error {
	for _, rule := range rules {
		if !ruleMatches(rule, eventName, key) {
			continue
		}
		for _, target := range resolveTargets(rule) {
			if err := postEventTo(ctx, client, target, body); err != nil {
				return fmt.Errorf("deliver notification to %s: %w", target, err)
			}
		}
	}
	return nil
}

// complete finalizes a claimed fact. Claim-lost (lease expired mid-flight,
// owner/token mismatch) is warned + counted and NOT retried in-loop (D7).
func (r *EventOutboxRelay) complete(fact repository.EventOutboxRow) {
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	defer cancel()
	if err := r.repo.CompleteEventOutbox(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken); err != nil {
		r.logger.Warn("event outbox claim lost on complete", "fact", fact.ID, "err", err)
		telemetry.IncEventOutboxClaimLost(ctx)
		return
	}
	telemetry.IncEventOutboxDelivered(ctx)
	r.logger.Debug("event outbox fact delivered", "fact", fact.ID, "type", string(fact.EventType))
}

// retry reschedules a failed fact with exponential backoff. The terminal
// 'failed' decision lives in RetryEventOutbox (attempts >= maxAttempts);
// telemetry mirrors it here.
func (r *EventOutboxRelay) retry(fact repository.EventOutboxRow, cause error) {
	delay := eventOutboxBackoff(fact.Attempts)
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.repo.RetryEventOutbox(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken,
		cause.Error(), time.Now().UTC().Add(delay), r.maxAttempts)
	cancel()
	if err != nil {
		r.logger.Warn("event outbox retry persistence failed", "fact", fact.ID, "err", err)
		telemetry.IncEventOutboxClaimLost(context.Background())
		return
	}
	if fact.Attempts >= r.maxAttempts {
		telemetry.IncEventOutboxFailed(context.Background())
	} else {
		telemetry.IncEventOutboxRetried(context.Background())
	}
	r.logger.Warn("event outbox delivery deferred", "fact", fact.ID,
		"attempt", fact.Attempts, "retry_in", delay, "err", cause)
}

// eventOutboxBackoff mirrors the billingBackoff shape: 1s base, 2×, 5min cap,
// downward-only jitter [0.75, 1.0)×base (the in-package crypto/rand jitter
// from webhook.go — no cross-domain import; strictly faster than base, which
// shrinks the at-least-once window, D-7). Bounds are testable; exact values
// are not (random).
func eventOutboxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := time.Second
	for i := 1; i < attempt && base < eventOutboxMaxBackoff; i++ {
		base *= 2
	}
	if base > eventOutboxMaxBackoff {
		base = eventOutboxMaxBackoff
	}
	return jitter(base)
}

// prune removes delivered facts older than the configured delivered
// retention (default 24h) and terminal-failed facts older than the
// configured failed retention (default 7d). DELETE-style, no tombstones —
// nothing re-enqueues from this table (unlike audit governance there is no
// gap-scan, D6/GAP-3).
func (r *EventOutboxRelay) prune() {
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	defer cancel()
	removed, err := r.repo.PruneEventOutbox(ctx, now.Add(-r.deliveredRetain), now.Add(-r.failedRetain))
	if err != nil {
		r.logger.Warn("event outbox prune failed", "err", err)
		return
	}
	if removed > 0 {
		telemetry.IncEventOutboxPruned(ctx, removed)
	}
}
