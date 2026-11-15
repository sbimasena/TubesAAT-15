package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// syncTimeout bounds one partition copy; partitions are small (dataset / 24).
const syncTimeout = 30 * time.Second

// movesPerPass keeps rebalancing gentle on the 1Gi store units.
const movesPerPass = 4

// LoadMap reads the durable partition map, creating it from defaultMap when absent.
// At creation the units reachable now are trusted as complete, so call CheckUnits first.
func (r *Router) LoadMap(ctx context.Context) error {
	r.mapMu.Lock()
	defer r.mapMu.Unlock()
	return r.loadMapLocked(ctx)
}

func (r *Router) loadMapLocked(ctx context.Context) error {
	m, version, found, err := r.maps.load(ctx)
	if err != nil {
		return fmt.Errorf("load partition map: %w", err)
	}
	if !found {
		m = r.current().m.clone()
		for _, u := range r.units {
			if live := u.incarnation.Load(); live != nil {
				m.Incarnations[u.name] = *live
			}
		}
		if version, err = r.maps.save(ctx, m, ""); err != nil {
			return fmt.Errorf("create partition map: %w", err)
		}
	}
	r.mapVersion = version
	r.layout.Store(r.resolve(m))
	return nil
}

// commitLocked saves m when it differs from the current map and switches to it.
func (r *Router) commitLocked(ctx context.Context, m partitionMap) error {
	before, _ := json.Marshal(r.current().m)
	after, _ := json.Marshal(m)
	if string(before) == string(after) {
		return nil
	}
	version, err := r.maps.save(ctx, m, r.mapVersion)
	if err != nil {
		return fmt.Errorf("save partition map: %w", err)
	}
	r.mapVersion = version
	r.layout.Store(r.resolve(m))
	return nil
}

// Reconcile repairs placement every interval until ctx ends: amnesia recovery,
// anti-entropy, re-replication of units unseen for deadAfter, and rebalancing.
// shortcut: not leader-gated yet (Aggregator runs 1 replica); call it only from the
// leader once internal/leader lands, passing the lease epoch.
func (r *Router) Reconcile(ctx context.Context, epoch int64, interval, deadAfter time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.reconcileOnce(ctx, epoch, deadAfter); err != nil && ctx.Err() == nil {
			r.log(slog.LevelWarn, "reconcile pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Router) reconcileOnce(ctx context.Context, epoch int64, deadAfter time.Duration) error {
	r.mapMu.Lock()
	defer r.mapMu.Unlock()
	if err := r.loadMapLocked(ctx); err != nil {
		return err
	}
	now := time.Now()
	dead := func(u *unit) bool { return now.Sub(time.Unix(0, u.lastSeen.Load())) >= deadAfter }
	l := r.current()
	m := l.m.clone()

	r.recoverAmnesia(ctx, epoch, l, m, dead)
	if err := r.commitLocked(ctx, m); err != nil {
		return err
	}
	// After amnesia: a refilled unit copied from one source, which may itself have lagged.
	r.antiEntropy(ctx, epoch, r.current(), dead)

	// Start moves and publish them first, so writes reach the new replica during the copy.
	m = r.current().m.clone()
	if planMoves(m, r.units, dead, r.current().serves) {
		if err := r.commitLocked(ctx, m); err != nil {
			return err
		}
	}
	l = r.current()
	m = l.m.clone()
	r.finishMoves(ctx, epoch, l, m, dead)
	return r.commitLocked(ctx, m)
}

// recoverAmnesia refills a unit whose live incarnation is not the recorded one (wiped
// disk, or new unit) from the other replicas, then records it so it serves reads again.
func (r *Router) recoverAmnesia(ctx context.Context, epoch int64, l *layout, m partitionMap, dead func(*unit) bool) {
	for _, u := range r.units {
		live := u.incarnation.Load()
		if live == nil || l.serves(u) || dead(u) {
			continue
		}
		complete := true
		for p := range Partitions {
			if !slices.Contains(l.writes[p], u) {
				continue
			}
			others := slices.DeleteFunc(slices.Clone(l.replicas[p]), func(o *unit) bool { return o == u })
			if len(others) == 0 {
				continue // sole copy (single-unit Compose): nothing to copy from
			}
			if !r.syncFromAny(ctx, epoch, p, others, u, l, dead) {
				complete = false
			}
		}
		if complete {
			m.Incarnations[u.name] = *live
			r.log(slog.LevelInfo, "store unit refilled; serving reads again", "unit", u.name, "incarnation", *live)
		}
	}
}

// syncFromAny copies partition p into dst from the first serving source that answers.
func (r *Router) syncFromAny(ctx context.Context, epoch int64, p int, sources []*unit, dst *unit, l *layout, dead func(*unit) bool) bool {
	for _, src := range sources {
		if src == dst || !l.serves(src) || dead(src) {
			continue
		}
		if err := r.syncPartition(ctx, epoch, p, src, dst); err != nil {
			r.log(slog.LevelWarn, "partition copy failed", "partition", p, "from", src.name, "to", dst.name, "error", err)
			continue
		}
		return true
	}
	return false
}

// antiEntropy compares (count, max ingested_at) between the replicas of every partition
// and copies both ways when they differ: the replica that was down catches up.
// shortcut: the digest misses an update that leaves count and max(ingested_at) equal;
// hash the rows if that shows up.
func (r *Router) antiEntropy(ctx context.Context, epoch int64, l *layout, dead func(*unit) bool) {
	for p, replicas := range l.replicas {
		live := slices.DeleteFunc(slices.Clone(replicas), func(u *unit) bool { return !l.serves(u) || dead(u) })
		if len(live) < 2 {
			continue
		}
		digests := gather(ctx, live, operationTimeout, func(ctx context.Context, u *unit) (partitionDigest, error) {
			return u.repo.digest(ctx, p)
		})
		for _, d := range digests[1:] {
			if d.err != nil || digests[0].err != nil || d.value == digests[0].value {
				continue
			}
			for _, pair := range [][2]*unit{{digests[0].unit, d.unit}, {d.unit, digests[0].unit}} {
				if err := r.syncPartition(ctx, epoch, p, pair[0], pair[1]); err != nil {
					r.log(slog.LevelWarn, "anti-entropy copy failed", "partition", p,
						"from", pair[0].name, "to", pair[1].name, "error", err)
				}
			}
		}
	}
}

// planMoves starts re-replication away from dead units and, when every unit is healthy,
// rebalancing toward the least loaded one. It reports whether m changed.
func planMoves(m partitionMap, units []*unit, dead func(*unit) bool, serves func(*unit) bool) bool {
	healthy := func(u *unit) bool { return !dead(u) && serves(u) }
	load := m.load()
	least := func(exclude []string) string {
		best := ""
		for _, u := range units {
			if healthy(u) && !slices.Contains(exclude, u.name) && (best == "" || load[u.name] < load[best]) {
				best = u.name
			}
		}
		return best
	}
	changed := false
	allHealthy := true
	for _, u := range units {
		if !healthy(u) {
			allHealthy = false
		}
		if !dead(u) {
			continue
		}
		for p, s := range m.Partitions {
			if s.Adding != "" || !slices.Contains(s.Replicas, u.name) {
				continue
			}
			if target := least(s.Replicas); target != "" {
				m.Partitions[p].Adding, m.Partitions[p].Removing = target, u.name
				load[target]++
				changed = true
			}
		}
	}
	for _, s := range m.Partitions {
		allHealthy = allHealthy && s.Adding == ""
	}
	for moves := 0; allHealthy && moves < movesPerPass; moves++ {
		heavy, light := "", ""
		for _, u := range units {
			if heavy == "" || load[u.name] > load[heavy] {
				heavy = u.name
			}
			if light == "" || load[u.name] < load[light] {
				light = u.name
			}
		}
		if load[heavy]-load[light] <= 1 {
			break
		}
		p := slices.IndexFunc(m.Partitions, func(s slot) bool {
			return s.Adding == "" && slices.Contains(s.Replicas, heavy) && !slices.Contains(s.Replicas, light)
		})
		if p < 0 {
			break
		}
		m.Partitions[p].Adding, m.Partitions[p].Removing = light, heavy
		load[light]++
		load[heavy]--
		changed = true
	}
	return changed
}

// finishMoves fills each Adding replica, then swaps it in for Removing. A move whose
// target died is dropped; planMoves picks another target next pass.
// shortcut: the old copy is left on Removing (reads ignore it via held); delete it if
// disk space on a unit becomes tight.
func (r *Router) finishMoves(ctx context.Context, epoch int64, l *layout, m partitionMap, dead func(*unit) bool) {
	for p, s := range m.Partitions {
		if s.Adding == "" {
			continue
		}
		dst := r.byName[s.Adding]
		if dst == nil || dead(dst) {
			m.Partitions[p].Adding, m.Partitions[p].Removing = "", ""
			continue
		}
		// Prefer the replica that stays; the leaving one may be dead.
		sources := slices.Clone(l.replicas[p])
		slices.SortStableFunc(sources, func(a, b *unit) int {
			return boolInt(a.name == s.Removing) - boolInt(b.name == s.Removing)
		})
		if len(sources) > 0 && !r.syncFromAny(ctx, epoch, p, sources, dst, l, dead) {
			continue
		}
		replicas := slices.DeleteFunc(slices.Clone(s.Replicas), func(n string) bool { return n == s.Removing })
		if i := slices.Index(s.Replicas, s.Removing); i >= 0 && i <= len(replicas) {
			replicas = slices.Insert(replicas, i, s.Adding) // keep the primary position
		} else {
			replicas = append(replicas, s.Adding)
		}
		m.Partitions[p] = slot{Replicas: replicas}
		r.log(slog.LevelInfo, "partition moved", "partition", p, "from", s.Removing, "to", s.Adding)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// syncPartition copies every row of partition p from src into dst, newer ingested_at wins.
func (r *Router) syncPartition(ctx context.Context, epoch int64, p int, src, dst *unit) error {
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	events, err := src.repo.exportPartition(ctx, p)
	if err != nil {
		return fmt.Errorf("export from %s: %w", src.name, err)
	}
	copied, err := dst.repo.importPartition(ctx, epoch, events)
	if errors.Is(err, ErrFenced) {
		return err
	}
	if err != nil {
		return fmt.Errorf("import into %s: %w", dst.name, err)
	}
	if copied > 0 {
		r.log(slog.LevelInfo, "partition rows copied", "partition", p, "from", src.name, "to", dst.name, "rows", copied)
	}
	return nil
}
