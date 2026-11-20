package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
)

func TestCoastalDLQRequiresAllReplicasAndFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	urls := make([]string, 3)
	repos := make([]*PostgresRepository, 3)
	for i := range repos {
		urls[i], _ = testSchema(t, ctx)
		var err error
		repos[i], err = OpenPostgres(ctx, urls[i], nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func(i int) { repos[i].Close() }(i)
	}
	r, err := newRouter([]string{"c0", "c1", "c2"}, repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	msg := deadletter.Message{MessageID: "pantai:replication-check", Source: "SENSOR_PANTAI", Raw: []byte{0xff, 0x00, 'x'}, Reason: "contract_violation:invalid_utf8", ReceivedAt: time.Now().UTC(), Attempts: 1, Status: deadletter.StatusParked}
	target := r.current().writes[PartitionOf(msg.MessageID)][0]
	index := -1
	for i, repo := range repos {
		if repo == target.repo {
			index = i
		}
	}
	target.repo.Close()
	if err := r.RecordUnprocessed(ctx, 3, msg); err == nil {
		t.Fatal("one-copy coastal DLQ write reported durable success")
	}
	repos[index], err = OpenPostgres(ctx, urls[index], nil)
	if err != nil {
		t.Fatal(err)
	}
	target.repo = repos[index]
	for range 2 {
		if err := r.RecordUnprocessed(ctx, 3, msg); err != nil {
			t.Fatal(err)
		}
	}
	copies := 0
	for _, repo := range repos {
		var n int
		if err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM unprocessed_messages WHERE message_id=$1 AND raw=$2", msg.MessageID, msg.Raw).Scan(&n); err != nil {
			t.Fatal(err)
		}
		copies += n
	}
	if copies != 2 {
		t.Fatalf("expected exactly RF2 original payload copies, got %d", copies)
	}
	if err := r.Fence(ctx, 5); err != nil {
		t.Fatal(err)
	}
	msg.MessageID = "pantai:old-epoch"
	if err := r.RecordUnprocessed(ctx, 4, msg); !errors.Is(err, ErrFenced) {
		t.Fatalf("old epoch accepted: %v", err)
	}
	msg.Source = "UNSUPPORTED"
	if err := r.RecordUnprocessed(ctx, 5, msg); err == nil {
		t.Fatal("unknown source accepted")
	}
}
