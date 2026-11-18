package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int64
	file    string
	after   func(context.Context, pgx.Tx) error
}

var migrations = []migration{
	{version: 1, file: "migrations/001_canonical.sql"},
	{version: 2, file: "migrations/002_m2.sql", after: backfillPartitions},
	{version: 3, file: "migrations/003_cursor_seq.sql"},
	{version: 4, file: "migrations/004_unprocessed_messages.sql"}, // D-DLQ
	{version: 5, file: "migrations/005_field_reports.sql"},        // D-FR
	{version: 6, file: "migrations/006_field_report_dlq.sql"},
}

// migrationLockID serialises migrations when several Aggregator replicas start together.
const migrationLockID = 4031_0002

const operationTimeout = 3 * time.Second

// PostgresRepository is one storage unit (one store-N pod). The Router spreads
// partitions over units; reads here are scoped to the partitions the caller names.
type PostgresRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// newUnit creates the pool without connecting, so a dead unit never blocks startup.
func newUnit(databaseURL string, logger *slog.Logger) (*PostgresRepository, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL configuration")
	}
	config.MaxConns = 8
	config.ConnConfig.ConnectTimeout = operationTimeout
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("create canonical connection pool: %w", err)
	}
	return &PostgresRepository{pool: pool, logger: logger}, nil
}

// OpenPostgres opens one unit and migrates it before returning.
func OpenPostgres(ctx context.Context, databaseURL string, logger *slog.Logger) (*PostgresRepository, error) {
	repo, err := newUnit(databaseURL, logger)
	if err != nil {
		return nil, err
	}
	if err := repo.Ping(ctx); err != nil {
		repo.Close()
		return nil, fmt.Errorf("connect canonical store: %w", err)
	}
	if err := repo.migrate(ctx); err != nil {
		repo.Close()
		return nil, fmt.Errorf("migrate canonical store: %w", err)
	}
	return repo, nil
}

func (r *PostgresRepository) Close() { r.pool.Close() }

func (r *PostgresRepository) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return r.pool.Ping(ctx)
}

func (r *PostgresRepository) migrate(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return err
	}
	for _, m := range migrations {
		var applied bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)",
			m.version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationFiles.ReadFile(m.file)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if m.after != nil {
			if err := m.after(ctx, tx); err != nil {
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// backfillPartitions assigns partition_id to M1 rows; PostgreSQL has no fnv32a.
func backfillPartitions(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, "SELECT hazard_id FROM hazard_events WHERE partition_id IS NULL")
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, "UPDATE hazard_events SET partition_id=$2 WHERE hazard_id=$1",
			id, PartitionOf(id)); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "ALTER TABLE hazard_events ALTER COLUMN partition_id SET NOT NULL")
	return err
}

// checkFence implements D-FENCE inside a write transaction: zero rows means a newer
// epoch already wrote here. The row lock also orders this write against Fence.
const fenceSQL = "UPDATE fence SET epoch=$1 WHERE epoch <= $1"

func checkFence(ctx context.Context, tx pgx.Tx, epoch int64) error {
	tag, err := tx.Exec(ctx, fenceSQL, epoch)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrFenced
	}
	return nil
}

func (r *PostgresRepository) Fence(ctx context.Context, epoch int64) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, fenceSQL, epoch)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrFenced
	}
	return nil
}

const upsertSQL = `WITH changed AS (
    INSERT INTO hazard_events (
        hazard_id, source, source_ref_id, hazard_type, severity, area_name,
        latitude, longitude, occurred_at, ingested_at, attributes, partition_id
    ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$13)
    ON CONFLICT (hazard_id) DO UPDATE SET
        source = EXCLUDED.source, source_ref_id = EXCLUDED.source_ref_id,
        hazard_type = EXCLUDED.hazard_type, severity = EXCLUDED.severity,
        area_name = EXCLUDED.area_name, latitude = EXCLUDED.latitude,
        longitude = EXCLUDED.longitude, occurred_at = EXCLUDED.occurred_at,
        ingested_at = EXCLUDED.ingested_at, attributes = EXCLUDED.attributes,
        revision = hazard_events.revision + 1
    WHERE ROW(hazard_events.source, hazard_events.source_ref_id, hazard_events.hazard_type,
        hazard_events.severity, hazard_events.area_name, hazard_events.latitude,
        hazard_events.longitude, hazard_events.occurred_at, hazard_events.attributes)
    IS DISTINCT FROM ROW(EXCLUDED.source, EXCLUDED.source_ref_id, EXCLUDED.hazard_type,
        EXCLUDED.severity, EXCLUDED.area_name, EXCLUDED.latitude,
        EXCLUDED.longitude, EXCLUDED.occurred_at, EXCLUDED.attributes)
    RETURNING *
), queued AS (
    INSERT INTO hazard_outbox (hazard_id, hazard_revision, payload, correlation_id)
    SELECT hazard_id, revision, to_jsonb(changed) - 'revision' - 'partition_id', $12 FROM changed
    RETURNING payload
)
SELECT payload FROM queued`

func (r *PostgresRepository) Upsert(ctx context.Context, epoch int64, events []domain.HazardEvent, correlationID string) (changed []domain.HazardEvent, err error) {
	started := time.Now()
	defer func() { r.logCall("canonical-upsert", correlationID, started, err) }()
	changed = make([]domain.HazardEvent, 0, len(events))
	if len(correlationID) == 0 || len(correlationID) > 128 {
		return nil, errors.New("correlation ID must contain between 1 and 128 bytes")
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := checkFence(ctx, tx, epoch); err != nil {
		return nil, err
	}
	for _, event := range events {
		if event.OccurredAt.IsZero() || event.IngestedAt.IsZero() {
			return nil, errors.New("hazard timestamps must be set")
		}
		attributes, err := json.Marshal(event.Attributes)
		if err != nil {
			return nil, fmt.Errorf("encode hazard attributes: %w", err)
		}
		var payload []byte
		err = tx.QueryRow(ctx, upsertSQL, event.HazardID, event.Source, event.SourceRefID,
			event.HazardType, event.Severity, event.AreaName, event.Latitude, event.Longitude,
			event.OccurredAt, event.IngestedAt, attributes, correlationID,
			PartitionOf(event.HazardID)).Scan(&payload)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // Identical polling result: no update or outbox message.
		}
		if err != nil {
			return nil, fmt.Errorf("upsert hazard %s: %w", event.HazardID, err)
		}
		var stored domain.HazardEvent
		if err := json.Unmarshal(payload, &stored); err != nil {
			return nil, fmt.Errorf("decode canonical snapshot: %w", err)
		}
		changed = append(changed, stored)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return changed, nil
}

// Retractions remain in replication/export state but are not active hazards.
const activeHazardSQL = "NOT (source = 'SATELIT' AND attributes @> '{\"retracted\":true}'::jsonb)"

const eventColumns = `hazard_id, source, source_ref_id, hazard_type,
	severity, area_name, latitude, longitude, occurred_at, ingested_at, attributes`

func scanEvents(rows pgx.Rows) ([]domain.HazardEvent, error) {
	defer rows.Close()
	events := make([]domain.HazardEvent, 0)
	for rows.Next() {
		var event domain.HazardEvent
		if err := rows.Scan(&event.HazardID, &event.Source, &event.SourceRefID, &event.HazardType,
			&event.Severity, &event.AreaName, &event.Latitude, &event.Longitude,
			&event.OccurredAt, &event.IngestedAt, &event.Attributes); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func clampLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 100
	}
	return limit
}

// listParts serves the M1 read API: newest occurred_at first.
func (r *PostgresRepository) listParts(ctx context.Context, filter Filter, parts []int32) ([]domain.HazardEvent, error) {
	var since *time.Time
	if !filter.Since.IsZero() {
		since = &filter.Since
	}
	rows, err := r.pool.Query(ctx, `SELECT `+eventColumns+` FROM hazard_events
		WHERE `+activeHazardSQL+` AND partition_id = ANY($5::int[]) AND ($1 = '' OR source = $1) AND ($2 = '' OR hazard_type = $2)
		AND ($3::timestamptz IS NULL OR occurred_at >= $3)
		ORDER BY occurred_at DESC, hazard_id LIMIT $4`, filter.Source, filter.HazardType, since, clampLimit(filter.Limit), parts)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// pageCursor is the opaque Audit API cursor: the last (ingested_at, hazard_id) returned.
type pageCursor struct {
	IngestedAt time.Time `json:"t"`
	HazardID   string    `json:"h"`
}

func encodeCursor(c pageCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(value string) (*pageCursor, error) {
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	var c pageCursor
	if err != nil || json.Unmarshal(raw, &c) != nil || c.HazardID == "" {
		return nil, ErrInvalidCursor
	}
	return &c, nil
}

// queryParts returns up to limit events in Audit API order (ingested_at, hazard_id) after the cursor.
func (r *PostgresRepository) queryParts(ctx context.Context, filter Filter, after *pageCursor, parts []int32, limit int) ([]domain.HazardEvent, error) {
	var since, afterTime *time.Time
	var afterID string
	if !filter.IngestedSince.IsZero() {
		since = &filter.IngestedSince
	}
	if after != nil {
		afterTime, afterID = &after.IngestedAt, after.HazardID
	}
	rows, err := r.pool.Query(ctx, `SELECT `+eventColumns+` FROM hazard_events
		WHERE `+activeHazardSQL+` AND partition_id = ANY($7::int[]) AND ($1 = '' OR source = $1) AND ($2 = '' OR hazard_type = $2)
		AND ($3::timestamptz IS NULL OR ingested_at >= $3)
		AND ($4::timestamptz IS NULL OR (ingested_at, hazard_id) > ($4, $5))
		ORDER BY ingested_at, hazard_id LIMIT $6`,
		filter.Source, filter.HazardType, since, afterTime, afterID, limit, parts)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

type partRef struct {
	hazardID string
	ref      string
}

// refIDs feeds the checksum (kontrak-m2.md §7.3); an empty source covers every source.
func (r *PostgresRepository) refIDs(ctx context.Context, source string, parts []int32) ([]partRef, error) {
	rows, err := r.pool.Query(ctx, `SELECT hazard_id, source_ref_id FROM hazard_events
		WHERE `+activeHazardSQL+` AND partition_id = ANY($2::int[]) AND ($1 = '' OR source = $1)`, source, parts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (partRef, error) {
		var p partRef
		err := row.Scan(&p.hazardID, &p.ref)
		return p, err
	})
}

type analyticRow struct {
	hazardID, hazardType, severity, area string
	occurred                             time.Time
}

func (r *PostgresRepository) analyticRows(ctx context.Context, from, to time.Time, parts []int32) ([]analyticRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT hazard_id, hazard_type, severity, area_name, occurred_at FROM hazard_events
		WHERE `+activeHazardSQL+` AND partition_id = ANY($3::int[]) AND occurred_at >= $1 AND occurred_at < $2`, from, to, parts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (analyticRow, error) {
		var a analyticRow
		err := row.Scan(&a.hazardID, &a.hazardType, &a.severity, &a.area, &a.occurred)
		return a, err
	})
}

func checksumOf(source string, ids []string, asOf time.Time) ChecksumResult {
	sort.Strings(ids)
	digest := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return ChecksumResult{Source: source, Count: len(ids), Checksum: hex.EncodeToString(digest[:]),
		AsOf: asOf, UnavailableUnits: []string{}}
}

// locate reports whether this unit holds hazardID and the node it runs on
// (bnpb.node_name is set from the Downward API in deploy/k8s/base/store.yaml).
func (r *PostgresRepository) locate(ctx context.Context, hazardID string) (exists bool, node string, err error) {
	err = r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hazard_events WHERE `+activeHazardSQL+` AND hazard_id = $1),
		coalesce(current_setting('bnpb.node_name', true), '')`, hazardID).Scan(&exists, &node)
	return exists, node, err
}

func (r *PostgresRepository) saveCursor(ctx context.Context, epoch, seq int64, source, cursor string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := checkFence(ctx, tx, epoch); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingest_cursor (source, cursor_value, epoch, seq) VALUES ($1, $2, $3, $4)
		ON CONFLICT (source) DO UPDATE SET cursor_value = EXCLUDED.cursor_value,
		epoch = EXCLUDED.epoch, seq = EXCLUDED.seq, updated_at = CURRENT_TIMESTAMP`, source, cursor, epoch, seq); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type savedCursor struct {
	value      string
	epoch, seq int64
}

// loadCursor returns found=false when no cursor has been saved for source on this unit.
func (r *PostgresRepository) loadCursor(ctx context.Context, source string) (c savedCursor, found bool, err error) {
	err = r.pool.QueryRow(ctx, "SELECT cursor_value, epoch, seq FROM ingest_cursor WHERE source = $1", source).
		Scan(&c.value, &c.epoch, &c.seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

func (r *PostgresRepository) logCall(operation, correlationID string, started time.Time, err error) {
	if r.logger == nil {
		return
	}
	attrs := []any{"service", "aggregator", "target", "canonical-store", "operation", operation,
		"correlation_id", correlationID, "latency_ms", float64(time.Since(started).Microseconds()) / 1000}
	if err != nil {
		r.logger.Error("canonical operation failed", append(attrs, "error", err)...)
		return
	}
	r.logger.Info("canonical operation complete", attrs...)
}

// partitionDigest is the cheap anti-entropy fingerprint of one partition on one unit.
type partitionDigest struct {
	count       int64
	maxIngested int64 // unix micros; time.Time values from two units do not compare with ==
	reports     int64 // field reports are insert-only, so their count is enough
}

func (r *PostgresRepository) digest(ctx context.Context, p int) (partitionDigest, error) {
	var d partitionDigest
	var maxIngested *time.Time
	err := r.pool.QueryRow(ctx, `SELECT count(*), max(ingested_at),
		(SELECT count(*) FROM field_reports WHERE partition_id = $1)
		FROM hazard_events WHERE partition_id = $1`, p).
		Scan(&d.count, &maxIngested, &d.reports)
	if maxIngested != nil {
		d.maxIngested = maxIngested.UnixMicro()
	}
	return d, err
}

// exportPartition reads every row of partition p for a copy to another unit.
// shortcut: loads the partition in memory (dataset / 24 rows); stream it if partitions grow past ~50k rows.
func (r *PostgresRepository) exportPartition(ctx context.Context, p int) ([]domain.HazardEvent, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+eventColumns+` FROM hazard_events WHERE partition_id = $1`, p)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// importSQL keeps the newer copy and, unlike upsertSQL, queues no outbox message: the
// event was already published when it was first ingested.
const importSQL = `INSERT INTO hazard_events (
        hazard_id, source, source_ref_id, hazard_type, severity, area_name,
        latitude, longitude, occurred_at, ingested_at, attributes, partition_id
    ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12)
    ON CONFLICT (hazard_id) DO UPDATE SET
        source = EXCLUDED.source, source_ref_id = EXCLUDED.source_ref_id,
        hazard_type = EXCLUDED.hazard_type, severity = EXCLUDED.severity,
        area_name = EXCLUDED.area_name, latitude = EXCLUDED.latitude,
        longitude = EXCLUDED.longitude, occurred_at = EXCLUDED.occurred_at,
        ingested_at = EXCLUDED.ingested_at, attributes = EXCLUDED.attributes,
        revision = hazard_events.revision + 1
    WHERE EXCLUDED.ingested_at > hazard_events.ingested_at`

// importPartition writes copied rows under the fence; it returns how many rows changed.
func (r *PostgresRepository) importPartition(ctx context.Context, epoch int64, events []domain.HazardEvent) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := checkFence(ctx, tx, epoch); err != nil {
		return 0, err
	}
	batch := &pgx.Batch{}
	for _, e := range events {
		attributes, err := json.Marshal(e.Attributes)
		if err != nil {
			return 0, fmt.Errorf("encode hazard attributes: %w", err)
		}
		batch.Queue(importSQL, e.HazardID, e.Source, e.SourceRefID, e.HazardType, e.Severity, e.AreaName,
			e.Latitude, e.Longitude, e.OccurredAt, e.IngestedAt, attributes, PartitionOf(e.HazardID))
	}
	results := tx.SendBatch(ctx, batch)
	var copied int64
	for range events {
		tag, err := results.Exec()
		if err != nil {
			results.Close()
			return 0, err
		}
		copied += tag.RowsAffected()
	}
	if err := results.Close(); err != nil {
		return 0, err
	}
	return copied, tx.Commit(ctx)
}

// dropPartitionsExcept deletes the rows of partitions this unit no longer replicates,
// left behind by moves, field reports included. Events with an unpublished outbox message stay until the relay
// sends it; published messages of dropped events go with them (the FK needs that).
const dropSQL = `
    WITH gone AS (
        SELECT hazard_id FROM hazard_events e
        WHERE NOT (partition_id = ANY($1::int[]))
          AND NOT EXISTS (SELECT 1 FROM hazard_outbox o WHERE o.hazard_id = e.hazard_id AND o.published_at IS NULL)
    ), sent AS (
        DELETE FROM hazard_outbox WHERE hazard_id IN (SELECT hazard_id FROM gone)
    ), reports AS (
        DELETE FROM field_reports WHERE NOT (partition_id = ANY($1::int[]))
    )
    DELETE FROM hazard_events WHERE hazard_id IN (SELECT hazard_id FROM gone)`

func (r *PostgresRepository) dropPartitionsExcept(ctx context.Context, epoch int64, keep []int32) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := checkFence(ctx, tx, epoch); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, dropSQL, keep)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}
