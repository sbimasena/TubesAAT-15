package store

import (
	"context"
	"encoding/json"
	"time"
)

type OutboxMessage struct {
	MessageID     int64
	HazardID      string
	Revision      int64
	Payload       json.RawMessage
	CorrelationID string
	CreatedAt     time.Time
}

func (r *PostgresRepository) PendingOutbox(ctx context.Context) ([]OutboxMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT message_id, hazard_id, hazard_revision, payload,
		correlation_id, created_at FROM hazard_outbox WHERE published_at IS NULL ORDER BY message_id LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []OutboxMessage
	for rows.Next() {
		var message OutboxMessage
		if err := rows.Scan(&message.MessageID, &message.HazardID, &message.Revision,
			&message.Payload, &message.CorrelationID, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (r *PostgresRepository) MarkPublished(ctx context.Context, messageID int64) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	_, err := r.pool.Exec(ctx, `UPDATE hazard_outbox SET published_at=CURRENT_TIMESTAMP
		WHERE message_id=$1 AND published_at IS NULL`, messageID)
	return err
}
