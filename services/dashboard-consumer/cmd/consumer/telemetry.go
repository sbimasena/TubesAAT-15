package main

import (
	"context"
	"log/slog"
	"os"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// setupTracing exports spans over OTLP/HTTP to OTEL_EXPORTER_OTLP_ENDPOINT (Jaeger).
// Without the variable (Compose, tests) tracing stays a no-op.
func setupTracing(ctx context.Context, logger *slog.Logger) func(context.Context) error {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		logger.Warn("tracing disabled", "error", err)
		return func(context.Context) error { return nil }
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName(serviceName),
			semconv.K8SPodName(os.Getenv("HOSTNAME")))))
	otel.SetTracerProvider(provider)
	return provider.Shutdown
}

// headerCarrier lets the W3C propagator read traceparent from AMQP headers.
type headerCarrier amqp.Table

func (c headerCarrier) Get(key string) string {
	value, _ := c[key].(string)
	return value
}
func (c headerCarrier) Set(key, value string) { c[key] = value }
func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}

// consumeSpan continues the Aggregator's "outbox publish" trace for one delivery.
func consumeSpan(delivery amqp.Delivery) trace.Span {
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), headerCarrier(delivery.Headers))
	_, span := otel.Tracer(serviceName).Start(ctx, "consume "+queueName, trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("messaging.message.id", delivery.MessageId),
			attribute.String("correlation_id", delivery.CorrelationId)))
	return span
}
