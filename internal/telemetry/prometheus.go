package telemetry

import (
	"errors"
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
)

var (
	promHandlerMu sync.Mutex
	promHandler   http.Handler
)

func configuredPrometheusHandler() http.Handler {
	promHandlerMu.Lock()
	defer promHandlerMu.Unlock()
	return promHandler
}

func setConfiguredPrometheusHandler(h http.Handler) {
	promHandlerMu.Lock()
	defer promHandlerMu.Unlock()
	promHandler = h
}

func installPrometheusHandler() http.Handler {
	h := promhttp.Handler()
	setConfiguredPrometheusHandler(h)
	return h
}

func newPrometheusExporter() (*otelprom.Exporter, error) {
	return otelprom.New()
}

// EnablePrometheus returns the /metrics handler. When Setup already wired the
// Prometheus reader onto the SDK provider (Prometheus-only or mixed OTLP+
// Prometheus mode), this is a pure accessor. Otherwise it installs a
// Prometheus-only meter provider for no-OTLP callers.
//
// The metric SDK (v1.43) does not allow adding a Reader to an already-built
// *metric.MeterProvider. If an SDK provider already exists and Setup did not
// preconfigure Prometheus, fail closed instead of silently serving /metrics
// without the domain metrics.
func EnablePrometheus() (http.Handler, error) {
	if h := configuredPrometheusHandler(); h != nil {
		return h, nil
	}
	if _, ok := otel.GetMeterProvider().(*metric.MeterProvider); ok {
		return nil, errors.New("prometheus exporter must be enabled during telemetry setup when an SDK meter provider is already installed")
	}
	exp, err := newPrometheusExporter()
	if err != nil {
		return promhttp.Handler(), err
	}
	provider := metric.NewMeterProvider(metric.WithReader(exp))
	otel.SetMeterProvider(provider)
	return installPrometheusHandler(), nil
}
