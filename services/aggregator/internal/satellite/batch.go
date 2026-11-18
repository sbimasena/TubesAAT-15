package satellite

import (
	"context"
	"errors"
	"io"
	"time"
)

// The reader carries at most one outstanding frame. A commit or its retry
// dictates how far the next connection can resume.
func readBatches(ctx, stream context.Context, body io.Reader, offset int64, apply func(context.Context, []Frame) error) error {
	readCtx, cancel := context.WithCancel(stream)
	defer cancel()
	frames := make(chan Frame)
	result := make(chan error, 1)
	go func() {
		result <- ReadSSE(readCtx, body, offset, func(ctx context.Context, frame Frame) error {
			select {
			case frames <- frame:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	const maxFrames, maxBytes = 128, 2 << 20
	batch := make([]Frame, 0, maxFrames)
	size := 0
	var timer *time.Timer
	var deadline <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		timer.Stop()
		deadline = nil
		if err := apply(ctx, batch); err != nil {
			return err
		}
		clear(batch)
		batch, size = batch[:0], 0
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-result:
			if errors.Is(err, ErrProtocol) {
				return err
			}
			if commitErr := flush(); commitErr != nil {
				return commitErr
			}
			return err
		case <-deadline:
			if err := flush(); err != nil {
				return err
			}
		case frame := <-frames:
			if size+len(frame.Data) > maxBytes {
				if err := flush(); err != nil {
					return err
				}
			}
			if len(batch) == 0 {
				if timer == nil {
					timer = time.NewTimer(200 * time.Millisecond)
				} else {
					timer.Reset(200 * time.Millisecond)
				}
				deadline = timer.C
			}
			batch = append(batch, frame)
			size += len(frame.Data)
			if len(batch) == maxFrames {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
}
