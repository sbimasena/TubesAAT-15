package satellite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

// Opt in because this opens the group's single allowed practice connection.
func TestCoursePracticeFeed(t *testing.T) {
	if os.Getenv("SATELLITE_LIVE_TEST") != "1" {
		t.Skip("set SATELLITE_LIVE_TEST=1 for the course practice feed")
	}
	client, err := NewStreamClient(FeedURL, os.Getenv("SATELLITE_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	router := postgresRouter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session, err := client.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("practice session latest offset %d at virtual time %s", session.LatestOffset, session.VirtualNow)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	verify := func(ctx context.Context) error { return ctx.Err() }

	run := func(epoch, target int64) int64 {
		t.Helper()
		if err := router.Fence(ctx, epoch); err != nil {
			t.Fatal(err)
		}
		term, stop := context.WithCancel(ctx)
		defer stop()
		done := make(chan error, 1)
		go func() { done <- RunWorker(term, client, router, epoch, verify, logger) }()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case err := <-done:
				t.Fatalf("live worker exited before offset %d: %v", target, err)
			case <-ctx.Done():
				t.Fatalf("live run timed out before offset %d", target)
			case <-ticker.C:
				status := StoredStatus(ctx, router).(map[string]any)
				offset, ok := status["processed_offset"].(*int64)
				if !ok || offset == nil || *offset < target {
					continue
				}
				sum, err := router.Checksum(ctx, "SATELIT")
				if err != nil || sum.Count == 0 {
					t.Fatalf("expected active fires at %d: count=%d, err=%v", *offset, sum.Count, err)
				}
				t.Logf("epoch %d reached offset %d with %d active fires", epoch, *offset, sum.Count)
				stop()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return *offset
			}
		}
	}
	first := run(1, max(150, session.LatestOffset))
	run(2, first+30)

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range families {
		if metric.GetName() != "bnpb_satellite_ingest_duration_seconds" {
			continue
		}
		histogram := metric.Metric[0].GetHistogram()
		count := histogram.GetSampleCount()
		for _, bucket := range histogram.GetBucket() {
			if count > 0 && bucket.GetCumulativeCount() >= uint64(math.Ceil(.95*float64(count))) {
				t.Logf("live frame-to-commit p95 <= %.2f s (%d effects, disposable PostgreSQL)", bucket.GetUpperBound(), count)
				return
			}
		}
	}
	t.Fatal("no live commit latency samples")
}

// A capture supplies the exact same source prefix to uninterrupted and resumed
// processors, so the comparison is independent of the advancing virtual clock.
func TestRecordedPracticeReplay(t *testing.T) {
	path := os.Getenv("SATELLITE_CAPTURE")
	if path == "" {
		t.Skip("set SATELLITE_CAPTURE to a saved practice stream.sse")
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	ctx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	var changes []Mutation
	err = ReadSSE(ctx, input, -1, func(_ context.Context, frame Frame) error {
		if frame.Offset < 0 && frame.Kind == "heartbeat" {
			return nil
		}
		mutation, err := decodeFrame(frame)
		if err != nil {
			return err
		}
		changes = append(changes, mutation)
		return nil
	})
	if !errors.Is(err, io.EOF) || len(changes) < 2 {
		t.Fatalf("incomplete capture: %d changes, %v", len(changes), err)
	}

	start := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	verify := func(ctx context.Context) error { return ctx.Err() }
	replay := func(restart bool) (*State, store.ChecksumResult) {
		t.Helper()
		router := postgresRouter(t)
		processor, err := NewProcessor(router, 1, verify)
		if err != nil {
			t.Fatal(err)
		}
		if err := processor.Restore(ctx); err != nil {
			t.Fatal(err)
		}
		const batchSize = 32
		for i := 0; i < len(changes); i += batchSize {
			if restart && i == batchSize {
				if err := router.Fence(ctx, 2); err != nil {
					t.Fatal(err)
				}
				processor, err = NewProcessor(router, 2, verify)
				if err != nil {
					t.Fatal(err)
				}
				if err := processor.Restore(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := processor.Process(ctx, changes[i:min(i+batchSize, len(changes))], start.Add(time.Duration(i)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		result, err := router.Checksum(ctx, "SATELIT")
		if err != nil {
			t.Fatal(err)
		}
		return processor.checkpoint.State, result
	}
	expected, uninterrupted := replay(false)
	actual, resumed := replay(true)
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	actualJSON, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expectedJSON, actualJSON) || uninterrupted.Checksum != resumed.Checksum || uninterrupted.Count != resumed.Count {
		t.Fatalf("replayed state diverged: uninterrupted=%s resumed=%s", uninterrupted.Checksum, resumed.Checksum)
	}
	t.Logf("same recorded prefix: %d frames through offset %d, %d active fires, checksum %s", len(changes), actual.Offset, resumed.Count, resumed.Checksum)
}
