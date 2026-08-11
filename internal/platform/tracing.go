package platform

import (
	"context"
	"net/http"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the instrumentation scope every Omniflow span is recorded under.
const tracerName = "github.com/omniflow/omniflow"

// Tracer returns the shared tracer. With no exporter configured the global
// provider is a no-op, so instrumentation costs nothing until an operator wires
// one up.
func Tracer() trace.Tracer { return otel.Tracer(tracerName) }

// TracedHTTPClient wraps an outbound HTTP client so every provider and
// Remnawave call becomes a child span of the request or job that caused it.
// Only the method and host reach the span; URLs and bodies never do.
func TracedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
}

// TracedPool opens a pgx pool with query tracing attached. Statement text is
// recorded, and pgx never puts argument values into the span, so a traced query
// cannot leak a customer identifier or a secret.
func TracedPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.ConnConfig.Tracer = otelpgx.NewTracer(otelpgx.WithTrimSQLInSpanName())
	return pgxpool.NewWithConfig(ctx, config)
}

// StartSpan opens a span with bounded, non-identifying attributes. It is the
// single entry point for the Telegram and job instrumentation, so no caller has
// to decide what is safe to record.
func StartSpan(ctx context.Context, name string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attributes...))
}

// TelegramUpdateAttributes describes one Telegram update without identifying the
// customer behind it. The update ID correlates with the logs; the Telegram
// account never becomes a span attribute.
func TelegramUpdateAttributes(updateID int64, kind string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("messaging.system", "telegram"),
		attribute.String("omniflow.update.kind", kind),
		attribute.Int64("omniflow.update.id", updateID),
	}
}

// JobAttributes describes one durable job run.
func JobAttributes(kind string, attempt int) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("messaging.system", "river"),
		attribute.String("omniflow.job.kind", kind),
		attribute.Int("omniflow.job.attempt", attempt),
	}
}
