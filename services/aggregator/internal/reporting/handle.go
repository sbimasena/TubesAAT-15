package reporting

import (
	"context"
	"log/slog"
)

// ReportStore is the durable effect of the processor. The implementation is
// wired once member C's field_reports migration lands (D-FR: insert idempotent
// with unique report_id). SaveReport must:
//   - insert the report and return accepted=true on first sight of report_id;
//   - return accepted=false WITHOUT an error when report_id already exists,
//     which is how a redelivered message is recognized (the caller acks);
//   - return an error only for transient store failures, which makes the
//     message redelivered (nothing is lost).
type ReportStore interface {
	SaveReport(ctx context.Context, report Report, correlationID string) (accepted bool, err error)
}

// outcome is what the delivery handler decided for one message.
type outcome int

const (
	outcomeAck   outcome = iota // committed, duplicate, or parked: safe to ack
	outcomeRetry                // transient failure: nack with requeue
)

// delivery abstracts amqp.Delivery so the decision logic is unit-testable.
type delivery interface {
	Body() []byte
	CorrelationID() string
	// DeliveryCount exposes x-delivery-count on quorum queue redeliveries.
	DeliveryCount() int
	Ack(ctx context.Context) error
	Nack(ctx context.Context, requeue bool) error
}

// handle is the whole decision logic: parse → (dedup) durable effect → ack
// after commit, nack on transient failure. It never blocks on the broker.
func handle(ctx context.Context, log *slog.Logger, store ReportStore, message delivery) outcome {
	report, err := parseReport(message.Body())
	if err != nil {
		// A malformed payload can never succeed by redelivery. Until the DLQ
		// park table exists, the only safe options are looping forever or
		// dropping with a loud record; this chooses dropping with a log, and
		// the unprocessed_messages integration will replace the drop.
		if log != nil {
			log.Error("field report payload malformed, parked (DLQ integration pending)",
				"service", "aggregator", "queue", QueueName,
				"correlation_id", message.CorrelationID(), "error", err.Error())
		}
		if ackErr := message.Ack(ctx); ackErr != nil && log != nil {
			log.Warn("field report ack failed", "service", "aggregator", "queue", QueueName,
				"correlation_id", message.CorrelationID(), "error", ackErr.Error())
		}
		return outcomeAck
	}
	if count := message.DeliveryCount(); count > maxDeliveryAttempts {
		if log != nil {
			log.Error("field report exceeded redelivery attempts, parked (DLQ integration pending)",
				"service", "aggregator", "queue", QueueName,
				"correlation_id", message.CorrelationID(), "report_id", report.ReportID,
				"delivery_count", count)
		}
		_ = message.Ack(ctx)
		return outcomeAck
	}
	accepted, err := store.SaveReport(ctx, report, message.CorrelationID())
	if err != nil {
		// Transient store failure: redelivery keeps the report; the quorum
		// queue raises x-delivery-count so the park path above stays bounded.
		if log != nil {
			log.Warn("field report store failed, redelivering",
				"service", "aggregator", "queue", QueueName,
				"correlation_id", message.CorrelationID(), "report_id", report.ReportID,
				"error", err.Error())
		}
		_ = message.Nack(ctx, true)
		return outcomeRetry
	}
	if !accepted {
		// Duplicate redelivery: the report was already committed durably, so
		// ack without a second side effect (effectively-once).
		if log != nil {
			log.Info("field report duplicate skipped",
				"service", "aggregator", "queue", QueueName,
				"correlation_id", message.CorrelationID(), "report_id", report.ReportID)
		}
	}
	// Ack strictly AFTER the durable commit (D-FR ordering): if the processor
	// dies between insert and ack, redelivery hits the idempotent insert and
	// is skipped as a duplicate, never processed twice.
	if ackErr := message.Ack(ctx); ackErr != nil {
		if log != nil {
			log.Warn("field report ack failed after commit",
				"service", "aggregator", "queue", QueueName,
				"correlation_id", message.CorrelationID(), "report_id", report.ReportID,
				"error", ackErr.Error())
		}
		return outcomeRetry
	}
	return outcomeAck
}
