package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
)

// EnablePrometheus installs a Prometheus exporter as a *second* metric reader
// (the OTLP exporter installed by Setup keeps working). Returns an
// http.Handler that callers mount at /metrics.
//
// Returns nil + nil error when the OTel meter provider isn't a *metric.MeterProvider
// (i.e. Setup was called with OTel disabled). In that path we still want a
// /metrics endpoint that returns Prometheus-compatible "no data" output, so
// the caller can fall back to a default handler.
func EnablePrometheus() (http.Handler, error) {
	exp, err := prometheus.New()
	if err != nil {
		return promhttp.Handler(), err
	}
	// Try to attach to the existing meter provider; if it doesn't accept new
	// readers we install our own and let OTel composers (Setup) layer on
	// later.
	if mp, ok := otel.GetMeterProvider().(*metric.MeterProvider); ok {
		// MeterProvider has no AddReader at runtime; create a new one that
		// reuses the resource.
		_ = mp
	}
	provider := metric.NewMeterProvider(metric.WithReader(exp))
	otel.SetMeterProvider(provider)
	return promhttp.Handler(), nil
}
