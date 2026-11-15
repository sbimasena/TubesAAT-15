package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

func always(*unit) bool { return true }

func deadNamed(names ...string) func(*unit) bool {
	return func(u *unit) bool { return slices.Contains(names, u.name) }
}

// flip completes every pending move the way finishMoves does after a successful copy.
func flip(m partitionMap) {
	for p, s := range m.Partitions {
		if s.Adding == "" {
			continue
		}
		i := slices.Index(s.Replicas, s.Removing)
		replicas := slices.Clone(s.Replicas)
		replicas[i] = s.Adding
		m.Partitions[p] = slot{Replicas: replicas}
	}
}

func checkMap(t *testing.T, m partitionMap) {
	t.Helper()
	for p, s := range m.Partitions {
		if len(s.Replicas) != ReplicationFactor || s.Replicas[0] == s.Replicas[1] {
			t.Fatalf("partition %d replicas %v", p, s.Replicas)
		}
	}
}

func TestPlanMovesReReplicatesAwayFromDeadUnit(t *testing.T) {
	r := fakeRouter(t, 3)
	m := defaultMap([]string{"store-0", "store-1", "store-2"})
	if !planMoves(m, r.units, deadNamed("store-2"), always) {
		t.Fatal("a dead unit must start moves")
	}
	for p, s := range m.Partitions {
		holds := slices.Contains(s.Replicas, "store-2")
		if holds != (s.Removing == "store-2") || (holds && (s.Adding == "" || slices.Contains(s.Replicas, s.Adding))) {
			t.Fatalf("partition %d: %+v", p, s)
		}
	}
	flip(m)
	checkMap(t, m)
	// P1.6: the two survivors now hold every partition, so the next loss is survivable too.
	if load := m.load(); load["store-2"] != 0 || load["store-0"] != Partitions || load["store-1"] != Partitions {
		t.Fatalf("load after re-replication: %v", load)
	}
	if planMoves(m, r.units, deadNamed("store-2"), always) {
		t.Fatal("no rebalancing while a unit is dead")
	}
}

func TestPlanMovesRebalancesOntoNewUnit(t *testing.T) {
	r := fakeRouter(t, 4) // store-3 is new: in DATABASE_URL, absent from the map
	m := defaultMap([]string{"store-0", "store-1", "store-2"})
	for pass := 0; planMoves(m, r.units, deadNamed(), always); pass++ {
		pending := 0
		for _, s := range m.Partitions {
			if s.Adding != "" {
				pending++
			}
		}
		if pending > movesPerPass || pass > Partitions {
			t.Fatalf("pass %d started %d moves", pass, pending)
		}
		flip(m)
	}
	checkMap(t, m)
	for name, n := range m.load() {
		if n != Partitions*ReplicationFactor/4 {
			t.Fatalf("%s holds %d slots, want 12: %v", name, n, m.load())
		}
	}
}

func TestResolveAndMemoryMapCAS(t *testing.T) {
	r := fakeRouter(t, 3)
	m := defaultMap([]string{"store-0", "store-1"})
	m.Partitions[0].Adding, m.Partitions[0].Removing = "store-2", "store-1"
	l := r.resolve(m)
	if len(l.replicas[0]) != 2 || len(l.writes[0]) != 3 || l.writes[0][2].name != "store-2" {
		t.Fatalf("writes must include the replica being filled, reads must not: %v %v", l.replicas[0], l.writes[0])
	}
	if slices.Contains(l.held(r.units[2]), 0) {
		t.Fatal("a replica still being filled must not serve reads")
	}

	ctx := context.Background()
	store := &memoryMaps{}
	v1, err := store.save(ctx, m, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.save(ctx, m, ""); !errors.Is(err, errMapConflict) {
		t.Fatal("create must fail when a map exists")
	}
	if _, err := store.save(ctx, m, v1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.save(ctx, m, v1); !errors.Is(err, errMapConflict) {
		t.Fatal("a stale version must be rejected")
	}
}

// TestReconcileReReplicatesAndRebalances runs three units as schemas of one PostgreSQL.
func TestReconcileReReplicatesAndRebalances(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	names := []string{"store-0", "store-1", "store-2"}
	repos := make([]*PostgresRepository, len(names))
	for i := range names {
		databaseURL, _ := testSchema(t, ctx)
		repo, err := OpenPostgres(ctx, databaseURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(repo.Close)
		repos[i] = repo
	}
	r, err := newRouter(names, repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.CheckUnits(ctx)
	if err := r.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	var events []domain.HazardEvent
	var refs []string
	for i := range 40 {
		ref := fmt.Sprintf("rr-%02d", i)
		events = append(events, m2Event("BMKG", "SEISMIC", ref, base.Add(time.Duration(i)*time.Second)))
		refs = append(refs, ref)
	}
	if _, err := r.Upsert(ctx, 0, events, "reconcile-seed"); err != nil {
		t.Fatal(err)
	}
	wantSum := checksumOf("", refs, time.Time{}).Checksum

	// Map created while only store-0 answered: store-1 and store-2 share partitions with no
	// recorded replica, and must not wait on each other forever.
	m := r.current().m.clone()
	delete(m.Incarnations, "store-1")
	delete(m.Incarnations, "store-2")
	if err := r.commitLocked(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileOnce(ctx, 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := r.current().m.Incarnations; len(got) != 3 {
		t.Fatalf("unrecorded units must be refilled and recorded, got %v", got)
	}

	// Every unit holds exactly the events of the partitions it serves.
	checkPlacement := func(stage string) {
		t.Helper()
		l := r.current()
		for _, u := range r.units {
			var count int
			if err := u.repo.pool.QueryRow(ctx, "SELECT count(*) FROM hazard_events WHERE partition_id = ANY($1::int[])",
				l.held(u)).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			for _, e := range events {
				if slices.Contains(l.replicas[PartitionOf(e.HazardID)], u) {
					want++
				}
			}
			if count != want {
				t.Fatalf("%s: %s holds %d events, want %d", stage, u.name, count, want)
			}
		}
		if sum, err := r.Checksum(ctx, "BMKG"); err != nil || sum.Checksum != wantSum || sum.Partial {
			t.Fatalf("%s: checksum %+v err=%v", stage, sum, err)
		}
	}

	// store-2 unseen past the deadline: its slots move to the survivors (P1.6).
	r.units[2].lastSeen.Store(0)
	if err := r.reconcileOnce(ctx, 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if load := r.current().m.load(); load["store-2"] != 0 || load["store-0"] != Partitions {
		t.Fatalf("after re-replication: %v", load)
	}
	checkPlacement("re-replicated")

	// store-2 returns with its old data: rebalancing gives it back a third, a few moves per pass.
	r.CheckUnits(ctx)
	for pass := 0; ; pass++ {
		before := r.current().m.load()
		if err := r.reconcileOnce(ctx, 0, time.Minute); err != nil {
			t.Fatal(err)
		}
		if slices.Equal(sortedLoad(before), sortedLoad(r.current().m.load())) || pass > Partitions {
			break
		}
	}
	for name, n := range r.current().m.load() {
		if n != 16 {
			t.Fatalf("after rebalance %s holds %d slots: %v", name, n, r.current().m.load())
		}
	}
	checkPlacement("rebalanced")

	// The map is durable: a restarted Aggregator loads the moved placement, not the default.
	restarted, err := newRouter(names, repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.maps = r.maps
	restarted.CheckUnits(ctx)
	if err := restarted.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(restarted.current().m) != fmt.Sprint(r.current().m) {
		t.Fatal("restart must load the saved map")
	}
}

func sortedLoad(load map[string]int) []string {
	var out []string
	for name, n := range load {
		out = append(out, fmt.Sprintf("%s=%d", name, n))
	}
	slices.Sort(out)
	return out
}
