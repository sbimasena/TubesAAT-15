package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
)

func TestRecordPVMBGRequiresAllTargetsFenceAndDeduplicates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	urls := make([]string, 3)
	repos := make([]*PostgresRepository, 3)
	for i := range repos {
		urls[i], _ = testSchema(t, ctx)
		var err error
		if repos[i], err = OpenPostgres(ctx, urls[i], nil); err != nil {
			t.Fatal(err)
		}
		defer func(i int) { repos[i].Close() }(i)
	}
	r, err := newRouter([]string{"s0", "s1", "s2"}, repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"report_id":"bad","reported_at":123}`)
	msg := deadletter.Message{MessageID: "pvmbg:test", Source: "PVMBG", Raw: raw,
		Reason: deadletter.ReasonSchemaIncompatible, SourceRefID: "bad", ReceivedAt: time.Now().UTC(),
		Attempts: 1, Status: deadletter.StatusParked}
	targets := r.current().writes[PartitionOf(msg.MessageID)]
	if len(targets) != 2 {
		t.Fatalf("expected RF2, got %d", len(targets))
	}

	// One unreachable replica must hold the checkpoint even though the other commits.
	broken, index := targets[0], -1
	for i, repo := range repos {
		if repo == broken.repo {
			index = i
		}
	}
	broken.repo.Close()
	if err := r.RecordPVMBG(ctx, 3, msg); err == nil {
		t.Fatal("partial DLQ write reported durable success")
	}
	if repos[index], err = OpenPostgres(ctx, urls[index], nil); err != nil {
		t.Fatal(err)
	}
	broken.repo = repos[index]

	for range 2 { // retry after recovery fills the gap without duplicating
		if err := r.RecordPVMBG(ctx, 3, msg); err != nil {
			t.Fatal(err)
		}
	}
	copies := 0
	for _, repo := range repos {
		var n int
		if err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM unprocessed_messages WHERE message_id=$1", msg.MessageID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		copies += n
	}
	if copies != 2 {
		t.Fatalf("expected exactly two copies, got %d", copies)
	}
	var saved []byte
	var source, status, ref string
	if err := targets[1].repo.pool.QueryRow(ctx, `SELECT raw, source, status, source_ref_id
		FROM unprocessed_messages WHERE message_id=$1`, msg.MessageID).Scan(&saved, &source, &status, &ref); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, raw) || source != "PVMBG" || status != "parked" || ref != "bad" {
		t.Fatal("parked payload or metadata changed")
	}
	// Audit pages union the replicas, exclude recovered rows and never
	// return the raw payload. Use a second ID to exercise the page boundary.
	second := msg
	second.MessageID = "pvmbg:zz"
	second.Status = deadletter.StatusRetrying
	second.Reason = deadletter.ReasonTimeout
	nextRetry := time.Now().UTC().Add(time.Minute)
	second.NextRetryAt = &nextRetry
	if err := r.RecordPVMBG(ctx, 3, second); err != nil {
		t.Fatal(err)
	}
	recovered := msg
	recovered.MessageID = "pvmbg:recovered"
	recovered.Status = deadletter.StatusRecovered
	if err := r.RecordPVMBG(ctx, 3, recovered); err != nil {
		t.Fatal(err)
	}
	firstPage, err := r.Unprocessed(ctx, "PVMBG", "", 1)
	if err != nil || len(firstPage.Items) != 1 || firstPage.Items[0].MessageID != msg.MessageID || firstPage.NextCursor == nil {
		t.Fatalf("first audit page: %+v err=%v", firstPage, err)
	}
	lastPage, err := r.Unprocessed(ctx, "PVMBG", *firstPage.NextCursor, 1)
	if err != nil || len(lastPage.Items) != 1 || lastPage.Items[0].MessageID != second.MessageID ||
		!lastPage.Items[0].Retryable || lastPage.NextCursor != nil {
		t.Fatalf("last audit page: %+v err=%v", lastPage, err)
	}
	if _, err := r.Unprocessed(ctx, "", "%%", 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("invalid audit cursor: %v", err)
	}

	older := msg
	older.MessageID = "pvmbg:older"
	older.Status = deadletter.StatusRetrying
	older.NextRetryAt = &nextRetry
	if err := r.RecordPVMBG(ctx, 3, older); err != nil {
		t.Fatal(err)
	}
	newer := older
	newer.MessageID = "pvmbg:newer"
	newer.ReceivedAt = older.ReceivedAt.Add(time.Second)
	due := time.Now().UTC().Add(-time.Second)
	newer.NextRetryAt = &due
	if err := r.RecordPVMBG(ctx, 3, newer); err != nil {
		t.Fatal(err)
	}
	if got, err := r.PendingPVMBGReplay(ctx, time.Now().UTC()); err != nil || got != nil {
		t.Fatalf("newer report overtook its delayed predecessor: %+v, %v", got, err)
	}
	if got, err := r.PendingPVMBGReplay(ctx, nextRetry.Add(time.Second)); err != nil ||
		got == nil || got.MessageID != older.MessageID {
		t.Fatalf("oldest replay was not selected first: %+v, %v", got, err)
	}

	if err := r.Fence(ctx, 5); err != nil {
		t.Fatal(err)
	}
	msg.MessageID = "pvmbg:zombie"
	if err := r.RecordPVMBG(ctx, 4, msg); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale epoch: %v", err)
	}
}
