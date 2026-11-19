package ingest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
)

// RecordPVMBG must return success only after an idempotent durable write.
// The storage implementation must fence the write using epoch.
type pvmbgDeadletter interface {
	RecordPVMBG(ctx context.Context, epoch int64, message deadletter.Message) error
}

var errPVMBGMapperPanic = errors.New("PVMBG mapper panic")

func mapPVMBGRecord(record source.PVMBGRecord) (event domain.HazardEvent, err error) {
	if record.Invalid != nil {
		return event, record.Invalid
	}
	defer func() {
		if recover() != nil {
			err = errPVMBGMapperPanic
		}
	}()
	event, err = domain.MapVolcanic(record.Report, time.Now().UTC())
	if err != nil {
		err = fmt.Errorf("%w: %v", deadletter.ErrContractViolation, err)
	}
	return event, err
}

func (m *Manager) parkPVMBG(ctx context.Context, raw []byte, sourceRefID string, since time.Time, cause error) error {
	reason := pvmbgReason(cause)
	if m.pvmbgDLQ == nil {
		return fmt.Errorf("PVMBG DLQ not configured: %w", cause)
	}
	if m.verify != nil {
		if err := m.verify(ctx); err != nil {
			return err
		}
	}
	var position string
	if !since.IsZero() {
		position = since.UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC()
	decision := deadletter.Decide(deadletter.DefaultPolicy(), reason, 1, now)
	err := m.pvmbgDLQ.RecordPVMBG(ctx, m.epoch, deadletter.Message{
		MessageID:   fmt.Sprintf("pvmbg:%x", sha256.Sum256(raw)),
		Source:      "PVMBG",
		Raw:         raw,
		Reason:      reason,
		SourceRefID: sourceRefID,
		Position:    position,
		ReceivedAt:  now,
		Attempts:    1,
		Status:      decision.Status,
		NextRetryAt: decision.NextRetry,
	})
	if err != nil {
		return fmt.Errorf("record PVMBG dead letter: %w", err)
	}
	return nil
}
