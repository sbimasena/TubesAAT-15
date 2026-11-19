package ingest

import (
	"context"
	"errors"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type pvmbgReplayStore interface {
	PendingPVMBGReplay(context.Context, time.Time) (*deadletter.Message, error)
	ReplayPVMBGEvent(context.Context, int64, domain.HazardEvent, string) error
	SavePVMBGReplay(context.Context, int64, deadletter.Message) error
}

func (m *Manager) retryPVMBG(ctx context.Context, correlation string) error {
	backend, ok := m.pvmbgDLQ.(pvmbgReplayStore)
	if !ok || ctx.Err() != nil {
		return ctx.Err()
	}
	if m.verify != nil {
		if err := m.verify(ctx); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	msg, err := backend.PendingPVMBGReplay(ctx, now)
	if err != nil || msg == nil {
		return err
	}
	if msg.Status != deadletter.StatusRetrying || (msg.NextRetryAt != nil && msg.NextRetryAt.After(now)) {
		// Another copy already committed this state; heal any stale retry marker.
		return backend.SavePVMBGReplay(ctx, m.epoch, *msg)
	}
	event, err := mapPVMBGRecord(source.DecodePVMBGReplay(msg.Raw))
	if err == nil && msg.SourceRefID != "" && msg.SourceRefID != event.SourceRefID {
		err = deadletter.ErrContractViolation
	}
	if err == nil {
		err = backend.ReplayPVMBGEvent(ctx, m.epoch, event, correlation)
	}
	if ctx.Err() != nil || errors.Is(err, store.ErrFenced) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	msg.Attempts++
	if err == nil {
		msg.Status, msg.NextRetryAt = deadletter.StatusRecovered, nil
	} else {
		msg.Reason = pvmbgReason(err)
		decision := deadletter.Decide(deadletter.DefaultPolicy(), msg.Reason, msg.Attempts, time.Now().UTC())
		msg.Status, msg.NextRetryAt = decision.Status, decision.NextRetry
	}
	return backend.SavePVMBGReplay(ctx, m.epoch, *msg)
}

func pvmbgReason(err error) string {
	if errors.Is(err, errPVMBGMapperPanic) {
		return deadletter.ReasonInternalPanic
	}
	return deadletter.Classify(err)
}
