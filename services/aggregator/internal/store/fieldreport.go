package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// FieldReport is the durable form of one Field Team report (D-FR).
type FieldReport struct {
	ReportID      string
	HazardID      string // "" when the report is not linked to a hazard event
	Note          string
	AreaName      string // "" when unset
	OccurredAt    *time.Time
	Reporter      string
	ReceivedAt    time.Time
	CorrelationID string // "" when the broker message carried none
}

// ErrReportRejected reports a field report that violates the column checks;
// like the rest of the store errors it is surfaced without payload content.
var ErrReportRejected = errors.New("store: field report rejected by constraints")

// SaveReport inserts one field report idempotently. accepted=false means the
// report_id is already stored (a redelivered message): not an error, the
// processor acks. Errors are transient store failures; nothing is lost because
// the message stays queued until the insert commits.
func (r *PostgresRepository) SaveReport(ctx context.Context, report FieldReport) (accepted bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	partition := PartitionOf(report.ReportID)
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO field_reports
			(report_id, partition_id, hazard_id, note, area_name, occurred_at,
			 reporter, received_at, correlation_id)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8, NULLIF($9, ''))
		ON CONFLICT (report_id) DO NOTHING`,
		report.ReportID, partition, report.HazardID, report.Note, report.AreaName,
		report.OccurredAt, report.Reporter, report.ReceivedAt, report.CorrelationID)
	if err != nil {
		return false, fmt.Errorf("%w: %s", ErrReportRejected, summarizePgError(err))
	}
	return tag.RowsAffected() == 1, nil
}

// summarizePgError keeps driver internals out of logs: the class of the
// failure, not the row contents, is what the processor needs to retry.
func summarizePgError(err error) string {
	if err == nil {
		return "unknown"
	}
	const maxLen = 160
	if len(err.Error()) > maxLen {
		return err.Error()[:maxLen]
	}
	return err.Error()
}

// SaveReport routes the report by report_id (PartitionOf) and writes it to
// every replica of the owning partition (RF=2, same as hazard_events). The
// report counts as stored only once every write target committed; retries
// fill partial writes idempotently. Unlike Upsert there is no leader
// fence: competing consumers run on every replica, and the idempotent insert
// on the PRIMARY KEY makes duplicate writes harmless (D-FR contract).
func (r *Router) SaveReport(ctx context.Context, report FieldReport, correlationID string) (accepted bool, err error) {
	started := time.Now()
	defer func() {
		level := slog.LevelInfo
		if err != nil || !accepted {
			level = slog.LevelWarn
		}
		r.log(level, "canonical operation complete", "target", "canonical-store", "operation", "field-report-save",
			"correlation_id", correlationID, "latency_ms", float64(time.Since(started).Microseconds())/1000, "error", err)
	}()
	l := r.current()
	replicas := l.writes[PartitionOf(report.ReportID)]
	if len(replicas) == 0 {
		return false, fmt.Errorf("store: partition %d has no replica assigned", PartitionOf(report.ReportID))
	}
	results := gather(ctx, replicas, operationTimeout, func(ctx context.Context, u *unit) (bool, error) {
		return u.repo.SaveReport(ctx, report)
	})
	var failures []error
	inserted := false
	for _, res := range results {
		if res.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", res.unit.name, res.err))
			r.log(slog.LevelWarn, "field report replica write failed; replica now lags", "unit", res.unit.name,
				"correlation_id", correlationID, "error", res.err)
			continue
		}
		inserted = inserted || res.value
	}
	// No anti-entropy worker copies field reports yet. Require all write
	// targets to commit; retry fills partial writes idempotently.
	if len(failures) > 0 {
		return false, errors.Join(failures...)
	}
	return inserted, nil
}
