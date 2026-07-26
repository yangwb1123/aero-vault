package telemetry

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Domain metrics. These are created lazily (on first record) so they bind to
// whichever MeterProvider is installed at runtime — the OTLP and/or Prometheus
// reader set up by Setup/EnablePrometheus — rather than the no-op provider that
// exists at package-init time. They surface at /metrics with names like
// ai_requests_total, ai_cost_micros_total, reconcile_orphan_blobs_total, etc.
var (
	domainOnce              sync.Once
	mAIRequests             metric.Int64Counter
	mAITokens               metric.Int64Counter
	mAICostMicros           metric.Int64Counter
	mAIEmbedRequests        metric.Int64Counter
	mAIEmbedTokens          metric.Int64Counter
	mAISearchLatency        metric.Float64Histogram
	mAIEmbedLatency         metric.Float64Histogram
	mReconcileOrphanBlobs   metric.Int64Counter
	mReconcileDeleted       metric.Int64Counter
	mIdempotencyReplays     metric.Int64Counter
	mEventsDropped          metric.Int64Counter
	mIndexerSkip            metric.Int64Counter
	mJobsCompleted          metric.Int64Counter
	mJobsFailed             metric.Int64Counter
	mJobsRetried            metric.Int64Counter
	mScrubTotal             metric.Int64Counter
	mWebhookRetries         metric.Int64Counter
	mWebhookDelivery        metric.Int64Counter
	mWebhookDeliveryLatency metric.Float64Histogram
	mWebhookDeadLetter      metric.Int64Counter
	mStorageSizeMismatch    metric.Int64Counter
	mETagVerifyMismatch     metric.Int64Counter
	mPresignGenerated       metric.Int64Counter
	mPresignConsumed        metric.Int64Counter
	mMiddlewareDuration     metric.Float64Histogram
	mSQLQueryDuration      metric.Float64Histogram
	mSQLQueryCount         metric.Int64Counter
)

func initDomain() {
	domainOnce.Do(func() {
		m := otel.Meter("aero-vault/domain")
		mAIRequests, _ = m.Int64Counter("ai.requests")
		mAITokens, _ = m.Int64Counter("ai.tokens")
		mAICostMicros, _ = m.Int64Counter("ai.cost_micros")
		mAIEmbedRequests, _ = m.Int64Counter("ai.embed_requests")
		mAIEmbedTokens, _ = m.Int64Counter("ai.embed_tokens")
		mAISearchLatency, _ = m.Float64Histogram("ai.search.duration_ms")
		mAIEmbedLatency, _ = m.Float64Histogram("ai.embed.duration_ms")
		mReconcileOrphanBlobs, _ = m.Int64Counter("reconcile.orphan_blobs")
		mReconcileDeleted, _ = m.Int64Counter("reconcile.orphan_blobs_deleted")
		mIdempotencyReplays, _ = m.Int64Counter("idempotency.replays")
		mEventsDropped, _ = m.Int64Counter("events.dropped")
		mIndexerSkip, _ = m.Int64Counter("indexer.skip_total")
		mJobsCompleted, _ = m.Int64Counter("jobs.completed_total")
		mJobsFailed, _ = m.Int64Counter("jobs.failed_total")
		mJobsRetried, _ = m.Int64Counter("jobs.retried_total")
		mScrubTotal, _ = m.Int64Counter("scrub.total")
		mWebhookRetries, _ = m.Int64Counter("webhook.retries_total")
		mWebhookDelivery, _ = m.Int64Counter("webhook.delivery_total")
		mWebhookDeliveryLatency, _ = m.Float64Histogram("webhook.delivery_latency_ms")
		mWebhookDeadLetter, _ = m.Int64Counter("webhook.dead_letter_total")
		mStorageSizeMismatch, _ = m.Int64Counter("storage.size_mismatch_total")
		mETagVerifyMismatch, _ = m.Int64Counter("etag.verify_mismatch_total")
		mPresignGenerated, _ = m.Int64Counter("presign.generated_total")
		mPresignConsumed, _ = m.Int64Counter("presign.consumed_total")
		mMiddlewareDuration, _ = m.Float64Histogram("middleware.duration_ms")
		mSQLQueryDuration, _ = m.Float64Histogram("sql.query_duration_ms")
		mSQLQueryCount, _ = m.Int64Counter("sql.query_total")
	})
}

// RecordSQLQuery records the duration of a single SQL query, attributed by
// the operation name so slow queries can be identified per-query-pattern.
func RecordSQLQuery(ctx context.Context, op string, durMs float64) {
	initDomain()
	attrs := metric.WithAttributes(attribute.String("op", op))
	mSQLQueryDuration.Record(ctx, durMs, attrs)
	mSQLQueryCount.Add(ctx, 1, attrs)
}

// RecordAIUsage records token/cost domain metrics for one AI (chat) call,
// attributed by tenant and model so per-tenant spend can be dashboarded.
func RecordAIUsage(ctx context.Context, tenant, model string, promptTokens, completionTokens int, costMicros int64) {
	initDomain()
	attrs := metric.WithAttributes(
		attribute.String("tenant", tenant),
		attribute.String("model", model),
	)
	mAIRequests.Add(ctx, 1, attrs)
	mAITokens.Add(ctx, int64(promptTokens+completionTokens), attrs)
	mAICostMicros.Add(ctx, costMicros, attrs)
}

// RecordEmbedUsage records one embedding API call and its token usage (surfaced
// as ai_embed_requests_total / ai_embed_tokens_total, attributed by model), so
// embedding spend is observable alongside chat usage. tokens may be 0 when the
// provider doesn't report usage.
func RecordEmbedUsage(ctx context.Context, model string, tokens int) {
	initDomain()
	attrs := metric.WithAttributes(attribute.String("model", model))
	mAIEmbedRequests.Add(ctx, 1, attrs)
	if tokens > 0 {
		mAIEmbedTokens.Add(ctx, int64(tokens), attrs)
	}
}

// RecordSearchLatency records the wall-clock latency (ms) of one semantic-search
// retrieval, attributed by mode (vector|bm25|hybrid), as ai_search_duration_ms.
func RecordSearchLatency(ctx context.Context, mode string, ms float64) {
	initDomain()
	mAISearchLatency.Record(ctx, ms, metric.WithAttributes(attribute.String("mode", mode)))
}

// RecordEmbedLatency records the latency (ms) of one embedding API call, as
// ai_embed_duration_ms{model}.
func RecordEmbedLatency(ctx context.Context, model string, ms float64) {
	initDomain()
	mAIEmbedLatency.Record(ctx, ms, metric.WithAttributes(attribute.String("model", model)))
}

// RecordReconcileBlobs records the outcome of one reconcile orphan-blob sweep.
func RecordReconcileBlobs(ctx context.Context, found, deleted int) {
	initDomain()
	mReconcileOrphanBlobs.Add(ctx, int64(found))
	mReconcileDeleted.Add(ctx, int64(deleted))
}

// IncIdempotencyReplay counts one idempotent replay (a retried write served
// from the stored response instead of re-executing).
func IncIdempotencyReplay(ctx context.Context) {
	initDomain()
	mIdempotencyReplays.Add(ctx, 1)
}

// IncEventDropped counts one in-process event dropped due to subscriber
// backpressure (the durable copy in the DB remains the source of truth).
func IncEventDropped(ctx context.Context) {
	initDomain()
	mEventsDropped.Add(ctx, 1)
}

// IncIndexerSkip counts one indexer skip, attributed by reason (e.g.
// "unsupported", "error", "empty") so operators can observe how many objects
// are bypassed and why.
func IncIndexerSkip(ctx context.Context, reason string) {
	initDomain()
	mIndexerSkip.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// IncJobCompleted counts one successful job execution, attributed by job_type.
func IncJobCompleted(ctx context.Context, jobType string) {
	initDomain()
	mJobsCompleted.Add(ctx, 1, metric.WithAttributes(attribute.String("job_type", jobType)))
}

// IncJobFailed counts one permanently-failed job (max retries exhausted or no
// handler registered), attributed by job_type.
func IncJobFailed(ctx context.Context, jobType string) {
	initDomain()
	mJobsFailed.Add(ctx, 1, metric.WithAttributes(attribute.String("job_type", jobType)))
}

// IncJobRetried counts one transient job failure that will be retried,
// attributed by job_type.
func IncJobRetried(ctx context.Context, jobType string) {
	initDomain()
	mJobsRetried.Add(ctx, 1, metric.WithAttributes(attribute.String("job_type", jobType)))
}

// IncScrubResult records the outcome of an integrity scrub.
func IncScrubResult(ctx context.Context, status string) {
	initDomain()
	mScrubTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", status)))
}

// IncWebhookRetry records a webhook retry attempt, attributed by URL host.
func IncWebhookRetry(ctx context.Context, url string) {
	initDomain()
	mWebhookRetries.Add(ctx, 1, metric.WithAttributes(attribute.String("url", url)))
}

// RecordWebhookDelivery counts one webhook delivery attempt, attributed by
// URL and HTTP status code, so operators can observe per-endpoint success rates.
func RecordWebhookDelivery(ctx context.Context, url string, statusCode int) {
	initDomain()
	mWebhookDelivery.Add(ctx, 1, metric.WithAttributes(
		attribute.String("url", url),
		attribute.Int("status_code", statusCode),
	))
}

// RecordWebhookDeliveryLatency records the round-trip latency (ms) of one
// webhook POST attempt, attributed by URL.
func RecordWebhookDeliveryLatency(ctx context.Context, url string, ms float64) {
	initDomain()
	mWebhookDeliveryLatency.Record(ctx, ms, metric.WithAttributes(attribute.String("url", url)))
}

// IncWebhookDeadLetter counts one event that has exhausted its retry budget
// and entered the dead-letter state, attributed by URL.
func IncWebhookDeadLetter(ctx context.Context, url string) {
	initDomain()
	mWebhookDeadLetter.Add(ctx, 1, metric.WithAttributes(attribute.String("url", url)))
}

// RegisterWebhookQueueDepthGauge registers an observable gauge
// (webhook_retry_queue_depth) whose value is read from fn on each scrape.
// The url attribute distinguishes per-endpoint queue depths.
func RegisterWebhookQueueDepthGauge(fn func(context.Context) map[string]int64) {
	m := otel.Meter("aero-vault/domain")
	_, _ = m.Int64ObservableGauge("webhook.retry_queue_depth", metric.WithInt64Callback(
		func(ctx context.Context, o metric.Int64Observer) error {
			for url, depth := range fn(ctx) {
				o.Observe(depth, metric.WithAttributes(attribute.String("url", url)))
			}
			return nil
		}))
}

// TenantStorage is one tenant's storage usage, emitted by the storage gauges.
type TenantStorage struct {
	Tenant  string
	Bytes   int64
	Objects int64
}

// RegisterQueueDepthGauge registers an observable gauge (jobs_pending) whose
// value is read from fn on each scrape. Call once, after the meter provider is
// installed.
func RegisterQueueDepthGauge(fn func(context.Context) int64) {
	m := otel.Meter("aero-vault/domain")
	_, _ = m.Int64ObservableGauge("jobs.pending", metric.WithInt64Callback(
		func(ctx context.Context, o metric.Int64Observer) error {
			o.Observe(fn(ctx))
			return nil
		}))
}

// RegisterStorageClassGauge registers an observable gauge reading per-class
// object counts from fn on each scrape.
func RegisterStorageClassGauge(fn func(context.Context, string) map[string]int64) {
	m := otel.Meter("aero-vault/domain")
	_, _ = m.Int64ObservableGauge("storage.class_objects", metric.WithInt64Callback(
		func(ctx context.Context, o metric.Int64Observer) error {
			// Sample default tenant; multi-tenant setups should register per tenant.
			for cls, count := range fn(ctx, "default") {
				o.Observe(count, metric.WithAttributes(attribute.String("class", cls), attribute.String("tenant", "default")))
			}
			return nil
		}))
}

// storage_objects) sourced from fn on each scrape.
func RegisterStorageGauges(fn func(context.Context) []TenantStorage) {
	m := otel.Meter("aero-vault/domain")
	bytesG, _ := m.Int64ObservableGauge("storage.bytes")
	objsG, _ := m.Int64ObservableGauge("storage.objects")
	_, _ = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		for _, ts := range fn(ctx) {
			attr := metric.WithAttributes(attribute.String("tenant", ts.Tenant))
			o.ObserveInt64(bytesG, ts.Bytes, attr)
			o.ObserveInt64(objsG, ts.Objects, attr)
		}
		return nil
	}, bytesG, objsG)
}

// IncStorageSizeMismatch counts one Content-Length vs actual body size mismatch.
func IncStorageSizeMismatch(ctx context.Context) {
	initDomain()
	mStorageSizeMismatch.Add(ctx, 1)
}

// IncETagVerifyMismatch counts one ETag verification failure during GET.
func IncETagVerifyMismatch(ctx context.Context) {
	initDomain()
	mETagVerifyMismatch.Add(ctx, 1)
}

// IncPresignGenerated counts one presigned URL generation.
func IncPresignGenerated(ctx context.Context) {
	initDomain()
	mPresignGenerated.Add(ctx, 1)
}

// IncPresignConsumed counts one presigned URL consumption.
func IncPresignConsumed(ctx context.Context) {
	initDomain()
	mPresignConsumed.Add(ctx, 1)
}

// RecordMiddlewareLatency records the duration of a single middleware layer.
func RecordMiddlewareLatency(ctx context.Context, name string, dur time.Duration) {
	initDomain()
	mMiddlewareDuration.Record(ctx, float64(dur.Milliseconds()),
		metric.WithAttributes(attribute.String("middleware", name)))
}
