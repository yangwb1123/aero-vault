package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
)

// EnablePrometheus mounts a Prometheus exporter and returns an http.Handler
// callers mount at /metrics.
//
// The metric SDK (v1.43) does not allow adding a Reader to an already-built
// *metric.MeterProvider — NewMeterProvider's doc is explicit ("Readers cannot
// be added after a MeterProvider is created") and the provider's readers/
// resource are unexported, so they can't be re-attached to a fresh provider
// either. We therefore must not clobber an OTLP provider that Setup installed:
//
//   - If the global meter provider is already an SDK *metric.MeterProvider
//     (Setup ran with OTLP enabled), leave it in place so OTLP export keeps
//     working — we do NOT install a Prometheus-only provider over it. /metrics
//     still serves runtime metrics via the promhttp default registerer.
//     (Co-locating both readers on one provider requires Setup to wire the
//     Prometheus reader at construction time, which is out of scope here.)
//   - Otherwise (OTLP disabled — the global is the no-op/delegating provider),
//     build a Prometheus-backed SDK provider and install it globally so domain
//     metrics flow to /metrics.
func EnablePrometheus() (http.Handler, error) {
	exp, err := prometheus.New()
	if err != nil {
		return promhttp.Handler(), err
	}
	if _, ok := otel.GetMeterProvider().(*metric.MeterProvider); ok {
		// An OTLP SDK provider is already installed by Setup. Installing a
		// Prometheus-only provider here would silently replace it and drop OTLP
		// export. The SDK exposes no way to add a Reader to a live provider or
		// to recover its existing reader/resource, so we cannot merge the two
		// onto one provider from here. Preserve OTLP rather than clobber it; the
		// exporter (registered with the default registerer by prometheus.New)
		// still backs /metrics for runtime metrics.
		return promhttp.Handler(), nil
	}
	// No SDK provider installed (OTLP disabled): install one backed by the
	// Prometheus reader so domain metrics surface at /metrics.
	provider := metric.NewMeterProvider(metric.WithReader(exp))
	otel.SetMeterProvider(provider)
	return promhttp.Handler(), nil
}
