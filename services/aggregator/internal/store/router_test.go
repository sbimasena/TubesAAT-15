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

func fakeRouter(t *testing.T, n int) *Router {
	t.Helper()
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("store-%d", i)
	}
	r, err := newRouter(names, make([]*PostgresRepository, n), nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStaticMapSpreadsEvenly(t *testing.T) {
	r := fakeRouter(t, 3)
	for _, u := range r.units {
		primary := 0
		for _, replicas := range r.current().replicas {
			if replicas[0] == u {
				primary++
			}
		}
		// P1.1: no unit holds everything; with RF=2 over 3 units each holds 2/3.
		if held := len(r.current().held(u)); held != 16 || primary != 8 {
			t.Fatalf("%s holds %d partitions (primary %d), want 16 (8)", u.name, held, primary)
		}
	}
	for p, replicas := range r.current().replicas {
		if len(replicas) != 2 || replicas[0] == replicas[1] {
			t.Fatalf("partition %d replicas %v", p, replicas)
		}
	}
	if single := fakeRouter(t, 1); len(single.current().replicas[0]) != 1 {
		t.Fatal("one unit must hold every partition once (Compose / M1)")
	}
}

func TestPlanPartialOnlyWhenPartitionHasNoReplica(t *testing.T) {
	r := fakeRouter(t, 3)
	answer := func(down ...string) []result[struct{}] {
		var results []result[struct{}]
		for _, u := range r.units {
			var err error
			if slices.Contains(down, u.name) {
				err = errors.New("down")
			}
			results = append(results, result[struct{}]{unit: u, err: err})
		}
		return results
	}
	if pl := plan(r, r.current(), answer()); pl.partial || len(pl.unavailable) != 0 {
		t.Fatalf("all up: %+v", pl)
	}
	// One unit down: every partition still has a replica (kontrak §7, context-m2.md §10 no. 1).
	if pl := plan(r, r.current(), answer("store-0")); pl.partial || !slices.Equal(pl.unavailable, []string{"store-0"}) {
		t.Fatalf("one down: partial=%v unavailable=%v", pl.partial, pl.unavailable)
	}
	if pl := plan(r, r.current(), answer("store-0", "store-1")); !pl.partial {
		t.Fatal("two of three down must leave some partition without a replica")
	}
}

func TestOpenRouterNamesUnitsByHost(t *testing.T) {
	r, err := OpenRouter("postgres://u:p@store-0.store:5432/db, postgres://u:p@store-1.store:5432/db", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.units[0].name != "store-0" || r.units[1].name != "store-1" {
		t.Fatalf("names %s %s", r.units[0].name, r.units[1].name)
	}
	if _, err := OpenRouter("postgres://u:p@store-0.store/db,postgres://u:p@store-0.store/db", nil); err == nil {
		t.Fatal("duplicate unit names must be rejected")
	}
	if _, err := OpenRouter("not a url", nil); err == nil {
		t.Fatal("a URL without a host must be rejected")
	}
}

// TestRouterReplication runs three units as three schemas of one PostgreSQL.
func TestRouterReplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	for i := range 30 {
		ref := fmt.Sprintf("q-%02d", i)
		events = append(events, m2Event("BMKG", "SEISMIC", ref, base.Add(time.Duration(i)*time.Second)))
		refs = append(refs, ref)
	}
	changed, err := r.Upsert(ctx, 0, events, "router-insert")
	if err != nil || len(changed) != len(events) {
		t.Fatalf("upsert: changed=%d err=%v", len(changed), err)
	}
	if changed, err := r.Upsert(ctx, 0, events, "router-repoll"); err != nil || len(changed) != 0 {
		t.Fatalf("repoll: changed=%d err=%v", len(changed), err)
	}

	// Each event is on exactly its two replicas; no unit holds all of them.
	for i, u := range r.units {
		var count int
		if err := u.repo.pool.QueryRow(ctx, "SELECT count(*) FROM hazard_events").Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		for _, e := range events {
			if slices.Contains(r.current().replicas[PartitionOf(e.HazardID)], u) {
				want++
			}
		}
		if count != want || count == len(events) {
			t.Fatalf("unit %d holds %d events, want %d (of %d)", i, count, want, len(events))
		}
	}

	wantSum := checksumOf("", slices.Clone(refs), time.Time{}).Checksum
	readAll := func() Page {
		t.Helper()
		var all Page
		cursor := ""
		for {
			page, err := r.Query(ctx, Filter{Limit: 7}, cursor)
			if err != nil {
				t.Fatal(err)
			}
			all.Items = append(all.Items, page.Items...)
			all.Partial = all.Partial || page.Partial
			all.UnavailableUnits = page.UnavailableUnits
			if page.NextCursor == nil {
				return all
			}
			cursor = *page.NextCursor
		}
	}
	all := readAll()
	if len(all.Items) != len(events) || all.Partial || len(all.UnavailableUnits) != 0 {
		t.Fatalf("scatter-gather: %d items partial=%v", len(all.Items), all.Partial)
	}
	for i, e := range all.Items {
		if e.HazardID != events[i].HazardID {
			t.Fatalf("item %d = %s, want %s (ingested_at order)", i, e.HazardID, events[i].HazardID)
		}
	}
	if sum, err := r.Checksum(ctx, "BMKG"); err != nil || sum.Count != 30 || sum.Checksum != wantSum {
		t.Fatalf("checksum: %+v err=%v", sum, err)
	}
	if list, err := r.List(ctx, Filter{Limit: 5}, "router-list"); err != nil || len(list) != 5 || list[0].HazardID != events[29].HazardID {
		t.Fatalf("list: %v err=%v", list, err)
	}

	target := events[0].HazardID
	locations, partial, err := r.Locate(ctx, target)
	replicas := r.current().replicas[PartitionOf(target)]
	if err != nil || partial || len(locations) != 2 || locations[0].Unit != replicas[0].name ||
		locations[0].Role != "primary" || locations[1].Unit != replicas[1].name || locations[1].Role != "replica" {
		t.Fatalf("locate: %+v partial=%v err=%v", locations, partial, err)
	}

	// A primary that missed writes while down must not hide rows (review finding 1),
	// and a stale copy of a hazard loses to the newer one.
	lagging := r.current().replicas[PartitionOf(target)][0]
	for _, table := range []string{"hazard_outbox", "hazard_events"} {
		if _, err := lagging.repo.pool.Exec(ctx, "DELETE FROM "+table+" WHERE hazard_id = $1", target); err != nil {
			t.Fatal(err)
		}
	}
	stale := events[1]
	staleUnit := r.current().replicas[PartitionOf(stale.HazardID)][0]
	if _, err := staleUnit.repo.pool.Exec(ctx, `UPDATE hazard_events SET severity = 'NORMAL',
		ingested_at = ingested_at - interval '1 hour' WHERE hazard_id = $1`, stale.HazardID); err != nil {
		t.Fatal(err)
	}
	all = readAll()
	if len(all.Items) != len(events) || all.Items[1].HazardID != stale.HazardID || all.Items[1].Severity != stale.Severity {
		t.Fatalf("lagging primary: %d items, item 1 = %+v", len(all.Items), all.Items[1])
	}
	if sum, err := r.Checksum(ctx, "BMKG"); err != nil || sum.Count != 30 || sum.Checksum != wantSum {
		t.Fatalf("checksum with a lagging primary: %+v err=%v", sum, err)
	}
	if list, err := r.List(ctx, Filter{Limit: 100}, "router-lag"); err != nil || len(list) != len(events) {
		t.Fatalf("list with a lagging primary: %d err=%v", len(list), err)
	}

	// Newest (epoch, seq) wins over a unit holding an older copy.
	if err := r.SaveCursor(ctx, 0, "BMKG", "new"); err != nil {
		t.Fatal(err)
	}
	if err := repos[2].saveCursor(ctx, 0, 1, "BMKG", "old"); err != nil {
		t.Fatal(err)
	}
	if cursor, err := r.LoadCursor(ctx, "BMKG"); err != nil || cursor != "new" {
		t.Fatalf("cursor=%q err=%v", cursor, err)
	}

	// Fence reaches every unit.
	if err := r.Fence(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upsert(ctx, 2, events[:1], "router-zombie"); !errors.Is(err, ErrFenced) {
		t.Fatalf("zombie upsert: %v", err)
	}

	// Amnesia: store-2 comes back empty with a new incarnation and stops serving reads.
	r.CheckUnits(ctx)
	if _, err := repos[2].pool.Exec(ctx, "TRUNCATE hazard_events, hazard_outbox; UPDATE unit_meta SET incarnation = gen_random_uuid()"); err != nil {
		t.Fatal(err)
	}
	r.CheckUnits(ctx)
	if r.current().serves(r.units[2]) {
		t.Fatal("new incarnation must stop the unit serving reads")
	}
	if sum, err := r.Checksum(ctx, "BMKG"); err != nil || sum.Count != 30 || sum.Checksum != wantSum || sum.Partial {
		t.Fatalf("checksum after amnesia: %+v err=%v", sum, err)
	}

	// One reconcile pass refills store-2 from its peers and repairs the lagging primary.
	if err := r.reconcileOnce(ctx, 3, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !r.current().serves(r.units[2]) {
		t.Fatal("reconciler must record store-2's new incarnation once refilled")
	}
	for _, u := range r.units {
		var count int
		if err := u.repo.pool.QueryRow(ctx, "SELECT count(*) FROM hazard_events WHERE partition_id = ANY($1::int[])",
			r.current().held(u)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		for _, e := range events {
			if slices.Contains(r.current().replicas[PartitionOf(e.HazardID)], u) {
				want++
			}
		}
		if count != want {
			t.Fatalf("after reconcile %s holds %d events, want %d", u.name, count, want)
		}
	}
	var outbox int
	if err := repos[2].pool.QueryRow(ctx, "SELECT count(*) FROM hazard_outbox").Scan(&outbox); err != nil || outbox != 0 {
		t.Fatalf("a copy must not queue outbox messages: %d err=%v", outbox, err)
	}

	// store-0 dies: every partition still has a live replica.
	repos[0].Close()
	if _, err := r.Upsert(ctx, 3, events, "router-refill"); err != nil {
		t.Fatalf("refill with one unit down: %v", err)
	}
	all = readAll()
	if len(all.Items) != len(events) || all.Partial || !slices.Equal(all.UnavailableUnits, []string{"store-0"}) {
		t.Fatalf("one unit down: %d items partial=%v unavailable=%v", len(all.Items), all.Partial, all.UnavailableUnits)
	}
	if sum, err := r.Checksum(ctx, "BMKG"); err != nil || sum.Checksum != wantSum || sum.Partial {
		t.Fatalf("checksum with one unit down: %+v err=%v", sum, err)
	}

	// store-1 dies too: partitions on {store-0, store-1} have no replica left.
	repos[1].Close()
	all = readAll()
	if !all.Partial || len(all.Items) == 0 || len(all.Items) == len(events) {
		t.Fatalf("two units down: %d items partial=%v", len(all.Items), all.Partial)
	}
	var lost domain.HazardEvent
	for _, e := range events {
		if !slices.Contains(r.current().replicas[PartitionOf(e.HazardID)], r.units[2]) {
			lost = e
			break
		}
	}
	if _, err := r.Upsert(ctx, 3, []domain.HazardEvent{lost}, "router-lost"); err == nil {
		t.Fatal("a write with no live replica must fail so the cursor does not advance")
	}
}
