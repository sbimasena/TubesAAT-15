package main

import (
	"context"
	"log/slog"
	"time"
)

const (
	leaderPrepareAttemptTimeout = 10 * time.Second
	leaderPrepareRetryInterval  = time.Second
)

// prepareLeaderTerm retries temporary storage failures without starting any workers.
// Each attempt re-verifies the lease and is bounded so a stale leader cannot write.
func prepareLeaderTerm(
	ctx context.Context,
	attemptTimeout time.Duration,
	retryInterval time.Duration,
	verify func(context.Context) error,
	prepare func(context.Context) error,
	logger *slog.Logger,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		err := verify(attemptCtx)
		if err == nil {
			err = prepare(attemptCtx)
		}
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warn("leader storage preparation unavailable; retrying", "error", err)
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
