package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestPrepareLeaderTermRetriesStorageAndReverifies(t *testing.T) {
	verifications, preparations := 0, 0
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := prepareLeaderTerm(
		context.Background(),
		time.Second,
		time.Millisecond,
		func(context.Context) error {
			verifications++
			return nil
		},
		func(context.Context) error {
			preparations++
			if preparations < 3 {
				return errors.New("store temporarily unavailable")
			}
			return nil
		},
		logger,
	)

	if err != nil {
		t.Fatalf("prepareLeaderTerm() error = %v", err)
	}
	if preparations != 3 || verifications != preparations {
		t.Fatalf("preparations=%d verifications=%d; want 3 attempts each", preparations, verifications)
	}
}

func TestPrepareLeaderTermStopsWhenTermIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	preparations := 0
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := prepareLeaderTerm(
		ctx,
		time.Second,
		time.Millisecond,
		func(context.Context) error { return nil },
		func(context.Context) error {
			preparations++
			cancel()
			return errors.New("store unavailable")
		},
		logger,
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prepareLeaderTerm() error = %v; want context cancellation", err)
	}
	if preparations != 1 {
		t.Fatalf("preparations = %d; want 1", preparations)
	}
}
