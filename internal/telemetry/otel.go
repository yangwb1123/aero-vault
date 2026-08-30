// Package telemetry initializes OpenTelemetry tracing + metrics with sensible
// defaults. When OTEL_EXPORTER_OTLP_ENDPOINT is unset and Prometheus is also
// disabled, it installs a no-op tracer/meter so collector-free deployments
// still run.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// Shutdown is called by main on SIGTERM to flush exporters.
type Shutdown func(context.Context) error

// Setup installs global tracer + meter providers. service is the service name
// used for the resource (defaults to "aero-vault"). When prometheusEnabled is
// true, the same meter provider exports domain metrics to /metrics as well.
// The returned Shutdown composes flush calls for every installed component.
func Setup(ctx context.Context, service string, logger *slog.Logger, prometheusEnabled bool) (Shutdown, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" && !prometheusEnabled {
		// No-op providers — global defaults already do nothing, but install
		// propagation so trace headers pass through.
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{},
		))
		logger.Info("otel disabled (OTEL_EXPORTER_OTLP_ENDPOINT unset)")
		return func(context.Context) error { return nil }, nil
	}

	if service == "" {
		service = "aero-vault"
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(service),
	))
	if err != nil {
		return nil, err
	}

	var (
		readers   []sdkmetric.Reader
		shutdowns []func(context.Context) error
	)
	if prometheusEnabled {
		promExp, err := newPrometheusExporter()
		if err != nil {
			return nil, err
		}
		readers = append(readers, promExp)
	}
	if endpoint != "" {
		traceExp, err := otlptracehttp.New(ctx, otlptracehttp.WithInsecure())
		if err != nil {
			return nil, err
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(5*time.Second)),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)

		metricExp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithInsecure())
		if err != nil {
			return nil, err
		}
		readers = append(readers, sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(15*time.Second)))
	}

	opts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	for _, reader := range readers {
		opts = append(opts, sdkmetric.WithReader(reader))
	}
	mp := sdkmetric.NewMeterProvider(opts...)
	otel.SetMeterProvider(mp)
	if prometheusEnabled {
		installPrometheusHandler()
	}
	shutdowns = append(shutdowns, mp.Shutdown)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	logger.Info("otel enabled", "endpoint", endpoint, "service", service, "prometheus", prometheusEnabled)

	return func(ctx context.Context) error {
		var errs []error
		for _, shutdown := range shutdowns {
			if err := shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}
