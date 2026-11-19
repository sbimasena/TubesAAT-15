package reporting

import (
	"context"
	"log/slog"
)

// ReportStore persists or parks a report before the processor acknowledges it.
// SaveReport must:
//   - insert the report and return accepted=true on first sight of report_id;
//   - return accepted=false WITHOUT an error when report_id already exists,
//     which is how a redelivered message is recognized (the caller acks);
//   - return an error only for transient store failures, which makes the
//     message redelivered (nothing is lost).
type ReportStore interface {
	SaveReport(ctx context.Context, report Report, correlationID string) (accepted bool, err error)
	ParkReport(ctx context.Context, raw []byte, reason string, attempts int) error
}

// outcome is what the delivery handler decided for one message.
type outcome int

const (
	outcomeAck   outcome = iota // committed, duplicate, or parked: safe to ack
	outcomeRetry                // retain delivery and retry without exhausting broker limits
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
		return park(ctx, log, store, message, "contract_violation")
	}
	if count := message.DeliveryCount(); count > maxDeliveryAttempts {
		return park(ctx, log, store, message, "store_unavailable")
	}
	accepted, err := store.SaveReport(ctx, report, message.CorrelationID())
	if err != nil {
		// Keep this delivery unacked and retry in place during store outages.
		if log != nil {
			log.Warn("field report store failed, redelivering",
				"service", "aggregator", "queue", QueueName,
				"correlation_id", message.CorrelationID(), "report_id", report.ReportID,
				"error", err.Error())
		}
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

func park(ctx context.Context, log *slog.Logger, store ReportStore, message delivery, reason string) outcome {
	if err := store.ParkReport(ctx, message.Body(), reason, message.DeliveryCount()); err != nil {
		if log != nil {
			log.Error("field report DLQ unavailable; retaining delivery", "reason", reason,
				"correlation_id", message.CorrelationID())
		}
		return outcomeRetry // leave unacked; consumer closes session and backs off
	}
	if log != nil {
		log.Warn("field report durably parked", "reason", reason, "correlation_id", message.CorrelationID())
	}
	if err := message.Ack(ctx); err != nil {
		return outcomeRetry
	}
	return outcomeAck
}
