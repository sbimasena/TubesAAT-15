package store

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// ParkReport retains the original bytes on EVERY current write target before
// permitting ack. unprocessed_messages rows have no anti-entropy copy yet, so
// accepting just one replica would falsely promise RF=2 durability.
// Content-derived IDs make redelivery idempotent even for malformed JSON.
func (r *Router) ParkReport(ctx context.Context, raw []byte, reason string, attempts int) error {
	id := fmt.Sprintf("field-report:%x", sha256.Sum256(raw))
	targets := r.current().writes[PartitionOf(id)]
	if len(targets) == 0 {
		return fmt.Errorf("field report DLQ has no write target")
	}
	results := gather(ctx, targets, operationTimeout, func(ctx context.Context, u *unit) (struct{}, error) {
		_, err := u.repo.pool.Exec(ctx, `INSERT INTO unprocessed_messages
			(message_id, partition_id, source, raw, reason, attempts, status, received_at, last_error_at)
			VALUES ($1, $2, 'FIELD_REPORT', $3, $4, $5, 'parked', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT (message_id) DO NOTHING`, id, PartitionOf(id), raw, reason, attempts)
		return struct{}{}, err
	})
	for _, result := range results {
		if result.err != nil {
			return fmt.Errorf("field report DLQ write failed on %s", result.unit.name)
		}
	}
	return nil
}
