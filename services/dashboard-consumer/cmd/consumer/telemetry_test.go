package main

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// A delivery published inside the Aggregator's span must join that trace.
func TestConsumeSpanContinuesPublisherTrace(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	delivery := amqp.Delivery{MessageId: "haz_1:abc", CorrelationId: "c-1",
		Headers: amqp.Table{"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}}
	span := consumeSpan(delivery)
	defer span.End()
	if got := span.SpanContext().TraceID().String(); got != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("trace id %s, want the publisher's", got)
	}
}
