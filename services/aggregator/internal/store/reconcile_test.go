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

// A follower routes by the map the leader saves: after a move it must stop writing to
// the unit the partition left (a dead unit would otherwise block field reports forever).
func TestFollowerReloadsMovedMap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader, follower := fakeRouter(t, 3), fakeRouter(t, 3)
	follower.maps = leader.maps
	if err := leader.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := follower.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	m := leader.current().m.clone()
	if !planMoves(m, leader.units, deadNamed("store-2"), always) {
		t.Fatal("a dead unit must start moves")
	}
	flip(m)
	leader.mapMu.Lock()
	err := leader.commitLocked(ctx, m)
	leader.mapMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	old := mapRefresh
	mapRefresh = 10 * time.Millisecond
	defer func() { mapRefresh = old }()
	go follower.followMap(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for fmt.Sprint(follower.current().m) != fmt.Sprint(m) {
		if time.Now().After(deadline) {
			t.Fatal("follower kept the old map")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for p, writes := range follower.current().writes {
		if slices.ContainsFunc(writes, func(u *unit) bool { return u.name == "store-2" }) {
			t.Fatalf("partition %d still writes to the moved-away unit", p)
		}
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
	// Field reports share the partitioning and must follow every move too (D-FR RF=2).
	var reportIDs []string
	for i := range 40 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		report := FieldReport{ReportID: id, Note: "uji", Reporter: "field-team", ReceivedAt: base}
		if _, err := r.SaveReport(ctx, report, "reconcile-seed"); err != nil {
			t.Fatal(err)
		}
		reportIDs = append(reportIDs, id)
	}
	// The relay has sent everything; sweep keeps events whose message is still pending.
	markSent := func() {
		t.Helper()
		for _, repo := range repos {
			if _, err := repo.pool.Exec(ctx, "UPDATE hazard_outbox SET published_at = now()"); err != nil {
				t.Fatal(err)
			}
		}
	}
	markSent()

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

	// Every unit holds exactly the events of the partitions it serves: moved-away copies are swept.
	checkPlacement := func(stage string) {
		t.Helper()
		l := r.current()
		for _, u := range r.units {
			if u.lastSeen.Load() == 0 {
				continue // dead: unreachable in reality, so nothing to sweep or to count
			}
			var count int
			if err := u.repo.pool.QueryRow(ctx, "SELECT count(*) FROM hazard_events").Scan(&count); err != nil {
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
			if err := u.repo.pool.QueryRow(ctx, "SELECT count(*) FROM field_reports").Scan(&count); err != nil {
				t.Fatal(err)
			}
			want = 0
			for _, id := range reportIDs {
				if slices.Contains(l.replicas[PartitionOf(id)], u) {
					want++
				}
			}
			if count != want {
				t.Fatalf("%s: %s holds %d field reports, want %d", stage, u.name, count, want)
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

	// An event with an unsent outbox message survives the sweep until the relay sends it.
	u := r.units[0]
	stray := m2Event("BMKG", "SEISMIC", "stray", base)
	for i := 0; slices.Contains(r.current().held(u), int32(PartitionOf(stray.HazardID))); i++ {
		stray = m2Event("BMKG", "SEISMIC", fmt.Sprintf("stray-%d", i), base)
	}
	if _, err := u.repo.Upsert(ctx, 0, []domain.HazardEvent{stray}, "stray"); err != nil {
		t.Fatal(err)
	}
	strayLeft := func() (n int) {
		t.Helper()
		r.sweep(ctx, 0, r.current(), func(*unit) bool { return false })
		if err := u.repo.pool.QueryRow(ctx, "SELECT count(*) FROM hazard_events WHERE hazard_id = $1", stray.HazardID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if strayLeft() != 1 {
		t.Fatal("sweep must keep an event whose outbox message is unsent")
	}
	markSent()
	if strayLeft() != 0 {
		t.Fatal("sweep must drop a moved-away event once its message is sent")
	}

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

func TestReconcileInitializesUnrecordedUnitsBeforeCursorRead(t *testing.T) {
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

	// Model a durable map created while all stores were temporarily unreachable.
	// The cursor is present on the stores, but none is trusted until reconciliation.
	if _, err := r.maps.save(ctx, defaultMap(names), ""); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Fence(ctx, 1); err != nil {
		t.Fatalf("Fence() error = %v", err)
	}
	if err := r.SaveCursor(ctx, 1, "BMKG", "cursor-after-restart"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LoadCursor(ctx, "BMKG"); err == nil {
		t.Fatal("cursor read must not trust units absent from the durable incarnation map")
	}

	leaderCtx := WithWriteVerifier(ctx, func(context.Context) error { return nil })
	if err := r.ReconcileIncarnations(leaderCtx, 1, time.Minute); err != nil {
		t.Fatalf("ReconcileIncarnations() error = %v", err)
	}
	if got := r.current().m.Incarnations; len(got) != len(names) {
		t.Fatalf("reconciliation did not record every live unit incarnation: %v", got)
	}
	cursor, err := r.LoadCursor(ctx, "BMKG")
	if err != nil {
		t.Fatalf("LoadCursor() after reconciliation: %v", err)
	}
	if cursor != "cursor-after-restart" {
		t.Fatalf("cursor = %q; want cursor-after-restart", cursor)
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

func TestStorageStatusCountsUnderReplicatedPartitions(t *testing.T) {
	r := fakeRouter(t, 3)
	if s := r.StorageStatus(); s.UnderReplicated != 0 || len(s.Units) != 3 || s.Units[0].Partitions != 16 {
		t.Fatalf("healthy cluster: %+v", s)
	}
	r.units[2].lastSeen.Store(0) // store-2 silent: every partition it holds is down to one copy
	if s := r.StorageStatus(); s.UnderReplicated != 16 || s.Units[2].Up {
		t.Fatalf("store-2 unreachable: %+v", s)
	}
}

func TestMissingFromListsOnlyAbsentOrOlderRows(t *testing.T) {
	src := partitionIndex{events: map[string]int64{"same": 1, "newer": 5, "absent": 1, "older": 1}, reports: map[string]bool{"r-same": true, "r-absent": true}}
	dst := partitionIndex{events: map[string]int64{"same": 1, "newer": 4, "older": 2}, reports: map[string]bool{"r-same": true}}
	events, reports := missingFrom(src, dst)
	slices.Sort(events)
	if !slices.Equal(events, []string{"absent", "newer"}) || !slices.Equal(reports, []string{"r-absent"}) {
		t.Fatalf("events=%v reports=%v", events, reports)
	}
}

func TestAntiEntropyCopiesOnlyDifferingRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	names := []string{"store-0", "store-1"}
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
	base := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	var events []domain.HazardEvent
	for i := range 30 {
		events = append(events, m2Event("BMKG", "SEISMIC", fmt.Sprintf("ae-%02d", i), base.Add(time.Duration(i)*time.Second)))
	}
	if _, err := r.Upsert(ctx, 0, events, "ae-seed"); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		report := FieldReport{ReportID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), Note: "uji", Reporter: "field-team", ReceivedAt: base}
		if _, err := r.SaveReport(ctx, report, "ae-seed"); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(repo *PostgresRepository, sql string, args ...any) {
		t.Helper()
		if _, err := repo.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// store-1 missed one write, store-0 missed a later revision of another and a report.
	lost, stale := events[3].HazardID, events[7].HazardID
	exec(repos[1], "DELETE FROM hazard_outbox WHERE hazard_id = $1", lost)
	exec(repos[1], "DELETE FROM hazard_events WHERE hazard_id = $1", lost)
	exec(repos[1], "UPDATE hazard_events SET area_name = 'Revisi', ingested_at = ingested_at + interval '1 hour' WHERE hazard_id = $1", stale)
	exec(repos[0], "DELETE FROM field_reports WHERE report_id = $1", "00000000-0000-4000-8000-000000000002")

	r.antiEntropy(ctx, 0, r.current(), func(*unit) bool { return false })

	snapshot := func(repo *PostgresRepository) string {
		t.Helper()
		var s string
		if err := repo.pool.QueryRow(ctx, `SELECT
			(SELECT string_agg(hazard_id || '|' || area_name || '|' || ingested_at::text, ',' ORDER BY hazard_id) FROM hazard_events) ||
			(SELECT string_agg(report_id, ',' ORDER BY report_id) FROM field_reports)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := snapshot(repos[0]), snapshot(repos[1])
	if a != b {
		t.Fatalf("replicas differ after anti-entropy:\nstore-0 %s\nstore-1 %s", a, b)
	}
	var area string
	var count int
	if err := repos[0].pool.QueryRow(ctx, "SELECT area_name, (SELECT count(*) FROM hazard_events) FROM hazard_events WHERE hazard_id = $1", stale).Scan(&area, &count); err != nil {
		t.Fatal(err)
	}
	if area != "Revisi" || count != len(events) {
		t.Fatalf("newer revision must win and the lost event return: area=%q count=%d", area, count)
	}
}
