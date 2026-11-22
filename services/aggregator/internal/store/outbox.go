package store

import (
	"context"
	"encoding/json"
	"time"
)

type OutboxMessage struct {
	Unit          string // store unit holding this row; MessageID is unique only within it.
	MessageID     int64
	HazardID      string
	Revision      int64
	Payload       json.RawMessage
	CorrelationID string
	CreatedAt     time.Time
	Critical      bool // coastal alert flagged critical by the sensor; published ahead of the backlog
}

func (r *PostgresRepository) PendingOutbox(ctx context.Context) ([]OutboxMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	// Critical coastal alerts jump the FIFO backlog (P5.1: <=5 s); everything else stays in order.
	// shortcut: the priority test scans pending rows without an index, add a partial index if
	// pending backlogs grow beyond tens of thousands of rows.
	rows, err := r.pool.Query(ctx, `SELECT message_id, hazard_id, hazard_revision, payload,
		correlation_id, created_at,
		COALESCE(payload->>'source' = 'SENSOR_PANTAI' AND payload->'attributes'->>'critical' = 'true', false)
		FROM hazard_outbox WHERE published_at IS NULL ORDER BY 7 DESC, message_id LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []OutboxMessage
	for rows.Next() {
		var message OutboxMessage
		if err := rows.Scan(&message.MessageID, &message.HazardID, &message.Revision,
			&message.Payload, &message.CorrelationID, &message.CreatedAt, &message.Critical); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (r *PostgresRepository) MarkPublished(ctx context.Context, message OutboxMessage) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	_, err := r.pool.Exec(ctx, `UPDATE hazard_outbox SET published_at=CURRENT_TIMESTAMP
		WHERE message_id=$1 AND published_at IS NULL`, message.MessageID)
	return err
}
