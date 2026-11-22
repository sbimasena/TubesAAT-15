package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type fakeOutbox struct{ marked []string }

func (f *fakeOutbox) PendingOutbox(context.Context) ([]store.OutboxMessage, error) { return nil, nil }
func (f *fakeOutbox) MarkPublished(_ context.Context, m store.OutboxMessage) error {
	f.marked = append(f.marked, m.Unit)
	return nil
}

// RF=2: both replicas queue the same snapshot. The broker must get it once, and both
// rows must be marked so neither is resent; new content is still published.
func TestDrainPublishesEachSnapshotOnce(t *testing.T) {
	copyOn := func(unit, payload string) store.OutboxMessage {
		return store.OutboxMessage{Unit: unit, HazardID: "haz_1", Payload: []byte(payload)}
	}
	batch := []store.OutboxMessage{copyOn("store-0", `{"r":1}`), copyOn("store-1", `{"r":1}`), copyOn("store-1", `{"r":2}`)}
	var published []string
	publish := func(_ context.Context, m store.OutboxMessage) error {
		published = append(published, string(m.Payload))
		return nil
	}
	repo := &fakeOutbox{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if !drain(context.Background(), batch, publish, repo, logger) {
		t.Fatal("drain failed")
	}
	if len(published) != 2 || published[0] != `{"r":1}` || published[1] != `{"r":2}` {
		t.Fatalf("published %v, want each snapshot once", published)
	}
	if len(repo.marked) != 3 {
		t.Fatalf("marked %v, want every replica's row", repo.marked)
	}

	// A failed publish stops the batch and leaves the rest pending for the next pass.
	repo = &fakeOutbox{}
	failing := func(context.Context, store.OutboxMessage) error { return errors.New("broker down") }
	if drain(context.Background(), batch, failing, repo, logger) || len(repo.marked) != 0 {
		t.Fatalf("failed publish must stop without marking, marked %v", repo.marked)
	}
}

// Units are merged one after another; a critical alert from the last unit must still be
// published before the full batches of the others.
func TestPrioritizeMovesCriticalAheadAndKeepsOrder(t *testing.T) {
	batch := []store.OutboxMessage{
		{Unit: "store-0", MessageID: 1}, {Unit: "store-0", MessageID: 2},
		{Unit: "store-1", MessageID: 7, Critical: true}, {Unit: "store-1", MessageID: 3},
		{Unit: "store-2", MessageID: 9, Critical: true},
	}
	prioritize(batch)
	var got []int64
	for _, m := range batch {
		got = append(got, m.MessageID)
	}
	if want := []int64{7, 9, 1, 2, 3}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

// The producer span's context must reach consumers through the AMQP headers.
func TestStringHeadersCarryTraceparent(t *testing.T) {
	headers := amqp.Table{"X-Correlation-ID": "c-1"}
	carrier := stringHeaders(headers)
	carrier.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	if headers["traceparent"] != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" || carrier.Get("X-Correlation-ID") != "c-1" || len(carrier.Keys()) != 2 {
		t.Fatalf("headers %v", headers)
	}
}
