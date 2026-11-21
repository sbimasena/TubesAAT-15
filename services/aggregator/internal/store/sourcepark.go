package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
)

// RecordUnprocessed exposes the existing fenced DLQ writer to ingestion sources.
// Like RecordPVMBG, every assigned copy must commit before a cursor can advance.
// DLQ anti-entropy is still a separate dependency; this does not implement it.
func (r *Router) RecordUnprocessed(ctx context.Context, epoch int64, msg deadletter.Message) error {
	switch msg.Source {
	case "BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI":
	default:
		return fmt.Errorf("unprocessed message has an unsupported source")
	}
	if msg.MessageID == "" || msg.Reason == "" || msg.ReceivedAt.IsZero() {
		return fmt.Errorf("unprocessed message is missing required metadata")
	}
	if err := verifyWrite(ctx); err != nil {
		return err
	}
	r.mapMu.Lock()
	defer r.mapMu.Unlock()
	p := PartitionOf(msg.MessageID)
	targets := r.current().writes[p]
	if len(targets) < min(ReplicationFactor, len(r.units)) || len(targets) == 0 {
		return fmt.Errorf("unprocessed partition %d is under-replicated", p)
	}
	results := gather(ctx, targets, operationTimeout, func(ctx context.Context, u *unit) (struct{}, error) {
		return struct{}{}, u.repo.recordUnprocessed(ctx, epoch, p, msg)
	})
	var failures []error
	for _, result := range results {
		if errors.Is(result.err, ErrFenced) {
			return ErrFenced
		}
		if result.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", result.unit.name, result.err))
		}
	}
	return errors.Join(failures...)
}
