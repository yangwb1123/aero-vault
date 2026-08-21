package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// RegisterAuditGovernanceMaxLagGauge registers the configured readiness lag
// boundary. Prometheus alert expressions use it to derive the early-warning
// age threshold, so changing AUDIT_GOVERNANCE_MAX_LAG_SECONDS does not leave
// a stale literal in deploy configuration.
func RegisterAuditGovernanceMaxLagGauge(fn func(context.Context) int64) {
	m := otel.Meter("aero-vault/domain")
	_, _ = m.Int64ObservableGauge("audit_governance.max_lag_seconds", metric.WithInt64Callback(
		func(ctx context.Context, o metric.Int64Observer) error {
			o.Observe(fn(ctx))
			return nil
		}))
}
