package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

//go:embed migrations/001_canonical.sql
var initialMigration string

const operationTimeout = 3 * time.Second

type PostgresRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
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
	repo := &PostgresRepository{pool: pool, logger: logger}
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
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return err
	}
	var applied bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 1)").Scan(&applied); err != nil {
		return err
	}
	// ponytail: one embedded migration for M1; use a migration tool when the stable schema grows.
	if !applied {
		if _, err := tx.Exec(ctx, initialMigration); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES (1)"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const upsertSQL = `WITH changed AS (
    INSERT INTO hazard_events (
        hazard_id, source, source_ref_id, hazard_type, severity, area_name,
        latitude, longitude, occurred_at, ingested_at, attributes
    ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
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
    SELECT hazard_id, revision, to_jsonb(changed) - 'revision', $12 FROM changed
    RETURNING payload
)
SELECT payload FROM queued`

func (r *PostgresRepository) Upsert(ctx context.Context, events []domain.HazardEvent, correlationID string) (changed []domain.HazardEvent, err error) {
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
			event.OccurredAt, event.IngestedAt, attributes, correlationID).Scan(&payload)
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

func (r *PostgresRepository) List(ctx context.Context, filter Filter, correlationID string) (events []domain.HazardEvent, err error) {
	started := time.Now()
	defer func() { r.logCall("canonical-list", correlationID, started, err) }()
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var since *time.Time
	if !filter.Since.IsZero() {
		since = &filter.Since
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT hazard_id, source, source_ref_id, hazard_type,
		severity, area_name, latitude, longitude, occurred_at, ingested_at, attributes
		FROM hazard_events
		WHERE ($1 = '' OR source = $1) AND ($2 = '' OR hazard_type = $2)
		AND ($3::timestamptz IS NULL OR occurred_at >= $3)
		ORDER BY occurred_at DESC, hazard_id LIMIT $4`, filter.Source, filter.HazardType, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events = make([]domain.HazardEvent, 0)
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
