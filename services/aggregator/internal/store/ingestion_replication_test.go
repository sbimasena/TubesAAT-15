package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

func TestIngestionRequiresReplicasBeforeCheckpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repos := make([]*PostgresRepository, 3)
	urls := make([]string, 3)
	for i := range repos {
		urls[i], _ = testSchema(t, ctx)
		var err error
		repos[i], err = OpenPostgres(ctx, urls[i], nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func(i int) { repos[i].Close() }(i)
	}
	r, err := newRouter([]string{"s0", "s1", "s2"}, repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event := domain.MapSeismic(domain.SeismicEvent{EventID: "replication-check", OccurredAt: now, RegionName: "Palu"}, nil, now)
	broken := r.current().writes[PartitionOf(event.HazardID)][0]
	broken.repo.Close()
	if err := r.Fence(ctx, 1); err != nil {
		t.Fatal("two live units must allow takeover:", err)
	}
	if _, err := r.UpsertReplicated(ctx, 1, []domain.HazardEvent{event}, "replicated-test"); err == nil {
		t.Fatal("one-copy write acknowledged")
	}
	if cursor, err := r.LoadCursor(ctx, "BMKG"); err != nil || cursor != "" {
		t.Fatalf("cursor advanced: %q %v", cursor, err)
	}
	if err := r.SaveCursorReplicated(ctx, 1, "SATELIT", "recovery-state"); err != nil {
		t.Fatal(err)
	}
	var other *unit
	for _, u := range r.units {
		if u != broken {
			other = u
			break
		}
	}
	other.repo.Close()
	if err := r.Fence(ctx, 2); err == nil {
		t.Fatal("isolated unit elected a fenced term")
	}
	if err := r.SaveCursorReplicated(ctx, 2, "SATELIT", "unsafe-state"); err == nil {
		t.Fatal("isolated checkpoint acknowledged")
	}
	// Reopen both pools. A retry fills partial copies without duplicate effects.
	for i, u := range r.units {
		repos[i], err = OpenPostgres(ctx, urls[i], nil)
		if err != nil {
			t.Fatal(err)
		}
		if u != broken && u != other {
			u.repo.Close()
		}
		u.repo = repos[i]
	}
	if _, err := r.UpsertReplicated(ctx, 2, []domain.HazardEvent{event}, "replicated-test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveCursor(ctx, 2, "BMKG", "committed"); err != nil {
		t.Fatal(err)
	}
	copies := 0
	for _, repo := range repos {
		var count int
		if err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM hazard_events WHERE hazard_id=$1", event.HazardID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		copies += count
	}
	if copies != 2 {
		t.Fatalf("copies=%d, want 2", copies)
	}
	denied := errors.New("lease lost")
	guarded := WithWriteVerifier(ctx, func(context.Context) error { return denied })
	if _, err := r.UpsertReplicated(guarded, 2, []domain.HazardEvent{event}, "guarded"); !errors.Is(err, denied) {
		t.Fatal(err)
	}
}

func TestMapMutationRechecksTerm(t *testing.T) {
	r := fakeRouter(t, 3)
	m := r.current().m.clone()
	m.Partitions[0].Replicas[0], m.Partitions[0].Replicas[1] = m.Partitions[0].Replicas[1], m.Partitions[0].Replicas[0]
	denied := errors.New("lease lost")
	ctx := WithWriteVerifier(context.Background(), func(context.Context) error { return denied })
	if err := r.commitLocked(ctx, m); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if r.current().m.Partitions[0].Replicas[0] == m.Partitions[0].Replicas[0] {
		t.Fatal("stale term published a partition map")
	}
}
