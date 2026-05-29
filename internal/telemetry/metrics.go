package telemetry

import (
	"context"
	"sync"

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
	domainOnce            sync.Once
	mAIRequests           metric.Int64Counter
	mAITokens             metric.Int64Counter
	mAICostMicros         metric.Int64Counter
	mReconcileOrphanBlobs metric.Int64Counter
	mReconcileDeleted     metric.Int64Counter
	mIdempotencyReplays   metric.Int64Counter
	mEventsDropped        metric.Int64Counter
)

func initDomain() {
	domainOnce.Do(func() {
		m := otel.Meter("aero-vault/domain")
		mAIRequests, _ = m.Int64Counter("ai.requests")
		mAITokens, _ = m.Int64Counter("ai.tokens")
		mAICostMicros, _ = m.Int64Counter("ai.cost_micros")
		mReconcileOrphanBlobs, _ = m.Int64Counter("reconcile.orphan_blobs")
		mReconcileDeleted, _ = m.Int64Counter("reconcile.orphan_blobs_deleted")
		mIdempotencyReplays, _ = m.Int64Counter("idempotency.replays")
		mEventsDropped, _ = m.Int64Counter("events.dropped")
	})
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

// RegisterStorageGauges registers per-tenant storage gauges (storage_bytes,
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
