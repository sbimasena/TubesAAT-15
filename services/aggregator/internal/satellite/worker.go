package satellite

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const FeedURL = "https://satellite.naufarrel.dev"

// RunWorker runs inside the current leader term. A failed store operation is
// recovered from the durable checkpoint before opening another stream.
func RunWorker(ctx context.Context, client *StreamClient, repo Repository, epoch int64, verify func(context.Context) error, logger *slog.Logger) error {
	processor, err := NewProcessor(repo, epoch, verify)
	if err != nil {
		return err
	}
	defer processorReady.Set(0)
	sessionCtx, cancelSession := context.WithCancel(ctx)
	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for sessionCtx.Err() == nil {
			session, err := client.Session(sessionCtx)
			if err == nil {
				processor.ObserveSession(session.LatestOffset, session.VirtualNow)
			} else if sessionCtx.Err() == nil {
				processor.session.Store(nil)
				offsetLag.Set(-1)
				eventLag.Set(-1)
				logger.Warn("Satellite session unavailable", "error", err)
			}
			select {
			case <-sessionCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() {
		cancelSession()
		<-sessionDone
	}()

	for ctx.Err() == nil {
		if err := verify(ctx); err != nil {
			return err
		}
		err = processor.Restore(ctx)
		if err == nil {
			err = client.Run(ctx, func(ctx context.Context) (int64, error) {
				if err := verify(ctx); err != nil {
					return -1, err
				}
				return processor.Offset()
			}, func(ctx context.Context, frames []Frame) error {
				batch := make([]Mutation, 0, len(frames))
				for _, frame := range frames {
					if frame.Kind == "heartbeat" && frame.Offset < 0 {
						continue
					}
					mutation, err := decodeFrame(frame)
					if err != nil {
						return err
					}
					batch = append(batch, mutation)
				}
				if len(batch) == 0 {
					return nil
				}
				return processor.process(ctx, batch, time.Now().UTC(), frames[0].ReceivedAt)
			})
		}
		processorReady.Set(0)
		var status *streamHTTPError
		if errors.Is(err, ErrProtocol) || errors.As(err, &status) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warn("Satellite checkpoint recovery scheduled", "error", err)
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
