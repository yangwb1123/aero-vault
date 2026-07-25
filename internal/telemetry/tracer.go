package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Tracer returns a named tracer from the global OTel provider.
// Packages should call this once at package init time and store the result:
//
//	var tracer = telemetry.Tracer("aero-vault/service")
//
// The stored tracer is then used to create child spans in methods like
// FileService.Get / Storage.Get / Repository.GetObject so the full request
// path becomes visible in distributed traces.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// SpanOptions is a convenience builder for creating spans with common
// attributes. Usage:
//
//	ctx, span := tracer.Start(ctx, "FileService.Get",
//	    telemetry.WithSpanAttrs("method", "Get", "bucket", b, "key", k),
//	)
//	defer span.End()
func WithSpanAttrs(kvs ...string) trace.SpanStartOption {
	attrs := make([]attribute.KeyValue, 0, len(kvs)/2)
	for i := 0; i+1 < len(kvs); i += 2 {
		attrs = append(attrs, attribute.String(kvs[i], kvs[i+1]))
	}
	return trace.WithAttributes(attrs...)
}
