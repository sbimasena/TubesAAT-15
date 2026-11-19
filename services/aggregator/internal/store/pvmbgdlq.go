package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
)

// RecordPVMBG keeps a failed PVMBG message on EVERY current write target of its
// partition, under the leader fence, before ingest may advance the checkpoint.
// Like ParkReport, one copy is not enough: unprocessed_messages has no
// anti-entropy yet. A repeated message_id is a no-op, so ingest retries freely.
func (r *Router) RecordPVMBG(ctx context.Context, epoch int64, msg deadletter.Message) error {
	if msg.Source != "PVMBG" {
		return fmt.Errorf("RecordPVMBG: source %q is not PVMBG", msg.Source)
	}
	if err := verifyWrite(ctx); err != nil {
		return err
	}
	p := PartitionOf(msg.MessageID)
	targets := r.current().writes[p]
	if len(targets) == 0 {
		return fmt.Errorf("PVMBG DLQ has no write target for partition %d", p)
	}
	results := gather(ctx, targets, operationTimeout, func(ctx context.Context, u *unit) (struct{}, error) {
		return struct{}{}, u.repo.recordUnprocessed(ctx, epoch, p, msg)
	})
	var failures []error
	for _, res := range results {
		if errors.Is(res.err, ErrFenced) {
			return ErrFenced
		}
		if res.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", res.unit.name, res.err))
		}
	}
	return errors.Join(failures...)
}

func (r *PostgresRepository) recordUnprocessed(ctx context.Context, epoch int64, partition int, msg deadletter.Message) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := checkFence(ctx, tx, epoch); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO unprocessed_messages
		(message_id, partition_id, source, source_ref_id, position, raw, reason, attempts, status,
		 next_retry_at, received_at, last_error_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $9, $10, $11, CURRENT_TIMESTAMP)
		ON CONFLICT (message_id) DO NOTHING`,
		msg.MessageID, partition, msg.Source, msg.SourceRefID, msg.Position, msg.Raw, msg.Reason,
		msg.Attempts, string(msg.Status), msg.NextRetryAt, msg.ReceivedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
