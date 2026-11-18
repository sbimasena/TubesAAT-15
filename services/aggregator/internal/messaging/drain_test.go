package messaging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

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
