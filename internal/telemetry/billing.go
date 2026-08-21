package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

func IncBillingRelayAttempted(ctx context.Context) {
	initDomain()
	mBillingRelayAttempted.Add(ctx, 1)
}

func IncBillingRelayDelivered(ctx context.Context) {
	initDomain()
	mBillingRelayDelivered.Add(ctx, 1)
}

func IncBillingRelayFailed(ctx context.Context) {
	initDomain()
	mBillingRelayFailed.Add(ctx, 1)
}

func IncBillingRelayDead(ctx context.Context) {
	initDomain()
	mBillingRelayDead.Add(ctx, 1)
}

// RegisterBillingBacklogAgeGauge registers a cache-fed billing backlog age.
func RegisterBillingBacklogAgeGauge(fn func(context.Context) int64) {
	m := otel.Meter("aero-vault/domain")
	_, _ = m.Int64ObservableGauge("billing.backlog_age_seconds", metric.WithInt64Callback(
		func(ctx context.Context, o metric.Int64Observer) error {
			o.Observe(fn(ctx))
			return nil
		}))
}
