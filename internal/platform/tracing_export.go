package platform

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// defaultTraceRatio samples one root trace in ten when the operator did not
// choose a sampler. Every query is a span, so tracing all traffic would cost
// more than it tells.
const defaultTraceRatio = 0.1

// SetupTracing installs an OTLP/HTTP exporter when the standard
// OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT variable is
// set, and leaves the global no-op provider in place otherwise. The exporter
// reads the rest of the standard OTEL_EXPORTER_OTLP_* variables (headers,
// timeout, compression) itself, and OTEL_TRACES_SAMPLER/_ARG choose the
// sampler. The returned function flushes and stops the exporter; it is safe
// to call when nothing was installed.
func SetupTracing(ctx context.Context, logger *slog.Logger, getenv func(string) string, service, version string) func(context.Context) error {
	noop := func(context.Context) error { return nil }
	if strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) == "" &&
		strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) == "" {
		return noop
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		logger.Error("trace exporter could not be built; tracing stays off", "error", err)
		return noop
	}
	if version == "" {
		version = "dev"
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", "omniflow-"+service),
		attribute.String("service.namespace", "omniflow"),
		attribute.String("service.version", version),
	)
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	}
	if strings.TrimSpace(getenv("OTEL_TRACES_SAMPLER")) == "" {
		options = append(options, sdktrace.WithSampler(
			sdktrace.ParentBased(sdktrace.TraceIDRatioBased(defaultTraceRatio))))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	logger.Info("trace export enabled", "service", service)
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return provider.Shutdown(ctx)
	}
}
