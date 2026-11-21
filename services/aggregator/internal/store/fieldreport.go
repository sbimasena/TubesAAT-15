package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		// Only SQLSTATE class 22 (data) / 23 (integrity) is a rejection; anything else
		// (unreachable unit, timeout) is a transient failure and must not read as one.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")) {
			return false, fmt.Errorf("%w: %s", ErrReportRejected, summarizePgError(err))
		}
		return false, fmt.Errorf("store: field report insert failed: %s", summarizePgError(err))
	}
	return tag.RowsAffected() == 1, nil
}

const reportColumns = `report_id, hazard_id, note, area_name, occurred_at, reporter, received_at, correlation_id`

// exportReports reads every field report of partition p for a partition copy, so a
// moved or refilled replica holds the reports too (RF=2 after re-replication).
func (r *PostgresRepository) exportReports(ctx context.Context, p int) ([]FieldReport, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+reportColumns+` FROM field_reports WHERE partition_id = $1`, p)
	if err != nil {
		return nil, err
	}
	return scanReports(rows)
}

func (r *PostgresRepository) exportReportsByID(ctx context.Context, ids []string) ([]FieldReport, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+reportColumns+` FROM field_reports WHERE report_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	return scanReports(rows)
}

func scanReports(rows pgx.Rows) ([]FieldReport, error) {
	defer rows.Close()
	var out []FieldReport
	for rows.Next() {
		var f FieldReport
		var hazardID, areaName, correlationID *string
		if err := rows.Scan(&f.ReportID, &hazardID, &f.Note, &areaName, &f.OccurredAt, &f.Reporter,
			&f.ReceivedAt, &correlationID); err != nil {
			return nil, err
		}
		f.HazardID, f.AreaName, f.CorrelationID = deref(hazardID), deref(areaName), deref(correlationID)
		out = append(out, f)
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// importReports inserts copied reports under the fence; reports are immutable, so an
// existing report_id is left as is. It returns how many reports were added.
func (r *PostgresRepository) importReports(ctx context.Context, epoch int64, reports []FieldReport) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := checkFence(ctx, tx, epoch); err != nil {
		return 0, err
	}
	var copied int64
	for _, f := range reports {
		tag, err := tx.Exec(ctx, `INSERT INTO field_reports (`+reportColumns+`, partition_id)
			VALUES ($1, NULLIF($2, ''), $3, NULLIF($4, ''), $5, $6, $7, NULLIF($8, ''), $9)
			ON CONFLICT (report_id) DO NOTHING`,
			f.ReportID, f.HazardID, f.Note, f.AreaName, f.OccurredAt, f.Reporter, f.ReceivedAt,
			f.CorrelationID, PartitionOf(f.ReportID))
		if err != nil {
			return 0, err
		}
		copied += tag.RowsAffected()
	}
	return copied, tx.Commit(ctx)
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
	// Require all write targets: the message stays in the quorum queue until both
	// replicas hold the report. If a unit is down, this waits until re-replication
	// (REREPLICATE_AFTER_SECONDS) swaps it out of the write set; syncPartition copies
	// the partition's reports to the new replica.
	if len(failures) > 0 {
		return false, errors.Join(failures...)
	}
	return inserted, nil
}
