package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

type pvmbgReplayKey struct{}

// ReplayPVMBGEvent uses the ingestion replication barrier but never replaces
// a same-age or newer canonical report with an older archived version.
func (r *Router) ReplayPVMBGEvent(ctx context.Context, epoch int64, event domain.HazardEvent, correlation string) error {
	_, err := r.UpsertReplicated(context.WithValue(ctx, pvmbgReplayKey{}, true), epoch, []domain.HazardEvent{event}, correlation)
	return err
}

// QueuePVMBGReplay is idempotent: already queued or recovered IDs are unchanged.
// Like the audit read, scan all configured units because DLQ rows do not move.
func (r *Router) QueuePVMBGReplay(ctx context.Context, epoch int64, ids []string) error {
	return r.updatePVMBG(ctx, epoch, `UPDATE unprocessed_messages SET status='retrying', next_retry_at=$2
		WHERE source='PVMBG' AND message_id=ANY($1) AND status='parked'`, ids, time.Now().UTC())
}

func (r *Router) SavePVMBGReplay(ctx context.Context, epoch int64, msg deadletter.Message) error {
	return r.updatePVMBG(ctx, epoch, `UPDATE unprocessed_messages
		SET attempts=$2, status=$3, next_retry_at=$4, reason=$5, last_error_at=CURRENT_TIMESTAMP
		WHERE source='PVMBG' AND message_id=$1 AND attempts <= $2 AND status <> 'recovered'`,
		msg.MessageID, msg.Attempts, string(msg.Status), msg.NextRetryAt, msg.Reason)
}

func (r *Router) updatePVMBG(ctx context.Context, epoch int64, sql string, args ...any) error {
	results := gather(ctx, r.units, operationTimeout, func(ctx context.Context, u *unit) (struct{}, error) {
		var empty struct{}
		if err := verifyWrite(ctx); err != nil {
			return empty, err
		}
		tx, err := u.repo.pool.Begin(ctx)
		if err != nil {
			return empty, err
		}
		defer tx.Rollback(ctx)
		if err := checkFence(ctx, tx, epoch); err != nil {
			return empty, err
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return empty, err
		}
		return empty, tx.Commit(ctx)
	})
	var err error
	for _, result := range results {
		err = errors.Join(err, result.err)
	}
	return err
}

// PendingPVMBGReplay returns only the oldest queued message for each known
// source_ref_id. A delayed retry therefore cannot be overtaken by a newer one.
// Polling with a one-record budget bounds payload memory and poller interference.
func (r *Router) PendingPVMBGReplay(ctx context.Context, now time.Time) (*deadletter.Message, error) {
	results := gather(ctx, r.units, unitReadTimeout, func(ctx context.Context, u *unit) ([]deadletter.Message, error) {
		return readPVMBGReplay(ctx, u, `SELECT DISTINCT ON (COALESCE(NULLIF(source_ref_id,''),message_id))
			message_id, COALESCE(source_ref_id,''), received_at, attempts, status, next_retry_at, reason, NULL::bytea
			FROM unprocessed_messages WHERE source='PVMBG' AND status='retrying'
			ORDER BY COALESCE(NULLIF(source_ref_id,''),message_id), received_at, message_id`)
	})
	heads := make(map[string]deadletter.Message)
	for _, result := range results {
		if result.err != nil {
			return nil, result.err
		}
		for _, msg := range result.value {
			ref := msg.SourceRefID
			if ref == "" {
				ref = msg.MessageID
			}
			old, ok := heads[ref]
			if !ok || replayOrder(msg, old) < 0 {
				heads[ref] = msg
			}
		}
	}
	due := make([]deadletter.Message, 0, len(heads))
	for _, msg := range heads {
		if msg.NextRetryAt != nil && !msg.NextRetryAt.After(now) {
			due = append(due, msg)
		}
	}
	if len(due) == 0 {
		return nil, nil
	}
	slices.SortFunc(due, replayOrder)
	results = gather(ctx, r.units, unitReadTimeout, func(ctx context.Context, u *unit) ([]deadletter.Message, error) {
		return readPVMBGReplay(ctx, u, `SELECT message_id, COALESCE(source_ref_id,''), received_at, attempts,
			status, next_retry_at, reason, raw FROM unprocessed_messages WHERE source='PVMBG' AND message_id=$1`,
			due[0].MessageID)
	})
	var latest *deadletter.Message
	for _, result := range results {
		if result.err != nil {
			return nil, result.err
		}
		for _, msg := range result.value {
			if latest == nil || msg.Attempts > latest.Attempts ||
				(msg.Attempts == latest.Attempts && replayRank(msg.Status) > replayRank(latest.Status)) {
				copy := msg
				latest = &copy
			}
		}
	}
	return latest, nil
}

func replayRank(status deadletter.Status) int {
	switch status {
	case deadletter.StatusRecovered:
		return 2
	case deadletter.StatusRetrying:
		return 1
	default:
		return 0
	}
}

func replayOrder(a, b deadletter.Message) int {
	return cmp.Or(a.ReceivedAt.Compare(b.ReceivedAt), cmp.Compare(a.MessageID, b.MessageID))
}

func readPVMBGReplay(ctx context.Context, u *unit, sql string, args ...any) ([]deadletter.Message, error) {
	rows, err := u.repo.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []deadletter.Message
	for rows.Next() {
		msg := deadletter.Message{Source: "PVMBG"}
		var status string
		if err := rows.Scan(&msg.MessageID, &msg.SourceRefID, &msg.ReceivedAt, &msg.Attempts,
			&status, &msg.NextRetryAt, &msg.Reason, &msg.Raw); err != nil {
			return nil, err
		}
		msg.Status = deadletter.Status(status)
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}
