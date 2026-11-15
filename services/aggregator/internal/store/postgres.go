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
}

// migrationLockID serialises migrations when several Aggregator replicas start together.
const migrationLockID = 4031_0002

const operationTimeout = 3 * time.Second

var (
	_ Store      = (*PostgresRepository)(nil)
	_ Repository = (*PostgresRepository)(nil)
)

// PostgresRepository is a single storage unit. Until the partition router lands it
// serves as the whole Store: every partition lives here with this unit as primary.
type PostgresRepository struct {
	pool     *pgxpool.Pool
	logger   *slog.Logger
	identity Location
}

func OpenPostgres(ctx context.Context, databaseURL string, logger *slog.Logger) (*PostgresRepository, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL configuration")
	}
	config.MaxConns = 8
	config.ConnConfig.ConnectTimeout = operationTimeout
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create canonical connection pool: %w", err)
	}
	repo := &PostgresRepository{pool: pool, logger: logger,
		identity: Location{Unit: "store-0", Role: "primary"}}
	if err := repo.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect canonical store: %w", err)
	}
	if err := repo.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate canonical store: %w", err)
	}
	return repo, nil
}

// SetIdentity names this unit and the Kubernetes node it runs on, as reported by Locate.
func (r *PostgresRepository) SetIdentity(unit, node string) {
	r.identity.Unit, r.identity.Node = unit, node
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

func (r *PostgresRepository) List(ctx context.Context, filter Filter, correlationID string) (events []domain.HazardEvent, err error) {
	started := time.Now()
	defer func() { r.logCall("canonical-list", correlationID, started, err) }()
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var since *time.Time
	if !filter.Since.IsZero() {
		since = &filter.Since
	}
	rows, err := r.pool.Query(ctx, `SELECT `+eventColumns+` FROM hazard_events
		WHERE ($1 = '' OR source = $1) AND ($2 = '' OR hazard_type = $2)
		AND ($3::timestamptz IS NULL OR occurred_at >= $3)
		ORDER BY occurred_at DESC, hazard_id LIMIT $4`, filter.Source, filter.HazardType, since, clampLimit(filter.Limit))
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
		return nil, errors.New("invalid cursor")
	}
	return &c, nil
}

// Query pages through events in Audit API order: ingested_at ascending, then hazard_id.
func (r *PostgresRepository) Query(ctx context.Context, filter Filter, cursor string) (Page, error) {
	after, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var since, afterTime *time.Time
	var afterID string
	if !filter.IngestedSince.IsZero() {
		since = &filter.IngestedSince
	}
	if after != nil {
		afterTime, afterID = &after.IngestedAt, after.HazardID
	}
	limit := clampLimit(filter.Limit)
	rows, err := r.pool.Query(ctx, `SELECT `+eventColumns+` FROM hazard_events
		WHERE ($1 = '' OR source = $1) AND ($2 = '' OR hazard_type = $2)
		AND ($3::timestamptz IS NULL OR ingested_at >= $3)
		AND ($4::timestamptz IS NULL OR (ingested_at, hazard_id) > ($4, $5))
		ORDER BY ingested_at, hazard_id LIMIT $6`,
		filter.Source, filter.HazardType, since, afterTime, afterID, limit+1)
	if err != nil {
		return Page{}, err
	}
	events, err := scanEvents(rows)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: events, UnavailableUnits: []string{}}
	if len(events) > limit {
		page.Items = events[:limit]
		last := page.Items[limit-1]
		next := encodeCursor(pageCursor{IngestedAt: last.IngestedAt, HazardID: last.HazardID})
		page.NextCursor = &next
	}
	return page, nil
}

// Checksum follows kontrak-m2.md §7.3; an empty source covers every source.
func (r *PostgresRepository) Checksum(ctx context.Context, source string) (ChecksumResult, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	asOf := time.Now().UTC()
	rows, err := r.pool.Query(ctx, "SELECT source_ref_id FROM hazard_events WHERE ($1 = '' OR source = $1)", source)
	if err != nil {
		return ChecksumResult{}, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return ChecksumResult{}, err
	}
	return checksumOf(source, ids, asOf), nil
}

func checksumOf(source string, ids []string, asOf time.Time) ChecksumResult {
	sort.Strings(ids)
	digest := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return ChecksumResult{Source: source, Count: len(ids), Checksum: hex.EncodeToString(digest[:]),
		AsOf: asOf, UnavailableUnits: []string{}}
}

func (r *PostgresRepository) Locate(ctx context.Context, hazardID string) ([]Location, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var exists bool
	if err := r.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM hazard_events WHERE hazard_id = $1)",
		hazardID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	return []Location{r.identity}, false, nil
}

func (r *PostgresRepository) SaveCursor(ctx context.Context, epoch int64, source, cursor string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := checkFence(ctx, tx, epoch); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingest_cursor (source, cursor_value, epoch) VALUES ($1, $2, $3)
		ON CONFLICT (source) DO UPDATE SET cursor_value = EXCLUDED.cursor_value,
		epoch = EXCLUDED.epoch, updated_at = CURRENT_TIMESTAMP`, source, cursor, epoch); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) LoadCursor(ctx context.Context, source string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var cursor string
	err := r.pool.QueryRow(ctx, "SELECT cursor_value FROM ingest_cursor WHERE source = $1", source).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return cursor, err
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
