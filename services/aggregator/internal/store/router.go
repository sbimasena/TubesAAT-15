package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("aggregator/store")

// ReplicationFactor is fixed by D-P1: with 3 units each holds 2/3 of the data.
const ReplicationFactor = 2

// unitReadTimeout bounds every scatter-gather leg (context-m2.md §9.2).
const unitReadTimeout = time.Second

var (
	_ Store      = (*Router)(nil)
	_ Repository = (*Router)(nil)
)

type unit struct {
	name        string
	repo        *PostgresRepository
	incarnation atomic.Pointer[string]
	node        atomic.Pointer[string] // bnpb.node_name of the store pod, for the status endpoint
	lastSeen    atomic.Int64           // unix nanos of the last successful check
}

// Router is the Store: it spreads Partitions over units with ReplicationFactor copies.
type Router struct {
	units     []*unit
	byName    map[string]*unit
	layout    atomic.Pointer[layout]
	cursorSeq atomic.Int64
	logger    *slog.Logger

	maps       mapStore
	mapMu      sync.Mutex // serialises map writers; version is the CAS token of layout
	mapVersion string
}

// OpenRouter takes DATABASE_URL as a comma-separated list, one URL per unit; the unit
// is named after the first DNS label of its host (store-1.store -> store-1).
func OpenRouter(databaseURLs string, logger *slog.Logger) (*Router, error) {
	var names []string
	var repos []*PostgresRepository
	for _, raw := range strings.Split(databaseURLs, ",") {
		raw = strings.TrimSpace(raw)
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" {
			closeAll(repos)
			return nil, errors.New("invalid DATABASE_URL configuration")
		}
		repo, err := newUnit(raw, logger)
		if err != nil {
			closeAll(repos)
			return nil, err
		}
		names = append(names, strings.SplitN(parsed.Hostname(), ".", 2)[0])
		repos = append(repos, repo)
	}
	router, err := newRouter(names, repos, logger)
	if err == nil {
		router.maps, err = newMapStore()
	}
	if err != nil {
		closeAll(repos)
	}
	return router, err
}

func closeAll(repos []*PostgresRepository) {
	for _, repo := range repos {
		repo.Close()
	}
}

// newRouter starts from defaultMap; LoadMap replaces it with the durable map.
func newRouter(names []string, repos []*PostgresRepository, logger *slog.Logger) (*Router, error) {
	r := &Router{byName: map[string]*unit{}, logger: logger}
	for i, name := range names {
		if _, dup := r.byName[name]; dup {
			return nil, fmt.Errorf("duplicate store unit name %q", name)
		}
		u := &unit{name: name, repo: repos[i]}
		u.lastSeen.Store(time.Now().UnixNano()) // grace period before a never-seen unit counts as dead
		r.units = append(r.units, u)
		r.byName[name] = u
	}
	// Seed from the clock so a restarted leader with the same epoch still writes a higher seq.
	r.cursorSeq.Store(time.Now().UnixNano())
	r.maps = &memoryMaps{}
	r.layout.Store(r.resolve(defaultMap(names)))
	return r, nil
}

func (r *Router) Close() {
	for _, u := range r.units {
		u.repo.Close()
	}
}

// Run keeps every unit migrated and watches its incarnation until ctx ends.
func (r *Router) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, u := range r.units {
		wg.Go(func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				r.check(ctx, u)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		})
	}
	wg.Wait()
}

// CheckUnits runs one migration/incarnation pass over all units; failures are logged only.
func (r *Router) CheckUnits(ctx context.Context) {
	var wg sync.WaitGroup
	for _, u := range r.units {
		wg.Go(func() { r.check(ctx, u) })
	}
	wg.Wait()
}

func (r *Router) check(ctx context.Context, u *unit) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	const query = "SELECT incarnation::text, coalesce(current_setting('bnpb.node_name', true), '') FROM unit_meta"
	var incarnation, node string
	err := u.repo.pool.QueryRow(ctx, query).Scan(&incarnation, &node)
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		// Fresh or wiped disk: no schema yet.
		if err = u.repo.migrate(ctx); err == nil {
			err = u.repo.pool.QueryRow(ctx, query).Scan(&incarnation, &node)
		}
	} else if err == nil {
		// Existing schema: catch up units left behind by a rolling restart.
		// migrate() is idempotent and serialized by the advisory lock, and
		// applies only the versions missing from schema_migrations.
		var version int64
		if vErr := u.repo.pool.QueryRow(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version); vErr == nil && version < int64(len(migrations)) {
			if mErr := u.repo.migrate(ctx); mErr != nil {
				r.log(slog.LevelWarn, "store unit migration failed", "unit", u.name, "error", mErr)
				return
			}
		}
	}
	if err != nil {
		r.log(slog.LevelWarn, "store unit unreachable", "unit", u.name, "error", err)
		return
	}
	u.lastSeen.Store(time.Now().UnixNano())
	u.node.Store(&node)
	previous := u.incarnation.Swap(&incarnation)
	if recorded := r.current().m.Incarnations[u.name]; recorded != "" && recorded != incarnation &&
		(previous == nil || *previous != incarnation) {
		r.log(slog.LevelWarn, "store unit lost its data; reads moved to replicas", "unit", u.name,
			"recorded_incarnation", recorded, "incarnation", incarnation)
	}
}

// UnitStat is one store unit's state for the metrics and status endpoints.
type UnitStat struct {
	Name       string `json:"unit"`
	Node       string `json:"node"`       // "" until the unit first answered
	Up         bool   `json:"reachable"`  // answered a health check within the last 15 s (checks run every 5 s)
	Serving    bool   `json:"serving"`    // false while refilled after data loss (amnesia)
	Partitions int    `json:"partitions"` // replica slots the partition map gives it (16 of 48 when balanced)
}

func (r *Router) UnitStats() []UnitStat {
	l := r.current()
	load := l.m.load()
	stats := make([]UnitStat, len(r.units))
	for i, u := range r.units {
		stats[i] = UnitStat{Name: u.name, Partitions: load[u.name], Serving: l.serves(u),
			Up: time.Since(time.Unix(0, u.lastSeen.Load())) < 15*time.Second}
		if node := u.node.Load(); node != nil {
			stats[i].Node = *node
		}
	}
	return stats
}

// StorageStatus is the storage section of GET /internal/v1/status.
type StorageStatus struct {
	Units []UnitStat `json:"units"`
	// Partitions with fewer than ReplicationFactor reachable, serving replicas right now.
	UnderReplicated int `json:"under_replicated_partitions"`
	MovesInProgress int `json:"moves_in_progress"`
}

func (r *Router) StorageStatus() StorageStatus {
	l := r.current()
	status := StorageStatus{Units: r.UnitStats()}
	healthy := map[string]bool{}
	for _, u := range status.Units {
		healthy[u.Name] = u.Up && u.Serving
	}
	for p, replicas := range l.replicas {
		live := 0
		for _, u := range replicas {
			if healthy[u.name] {
				live++
			}
		}
		if live < ReplicationFactor {
			status.UnderReplicated++
		}
		if l.m.Partitions[p].Adding != "" {
			status.MovesInProgress++
		}
	}
	return status
}

func (r *Router) log(level slog.Level, msg string, attrs ...any) {
	if r.logger != nil {
		r.logger.Log(context.Background(), level, msg, append([]any{"service", "aggregator"}, attrs...)...)
	}
}

type result[T any] struct {
	unit  *unit
	value T
	err   error
}

// gather runs fn on every unit in parallel, each bounded by timeout.
func gather[T any](ctx context.Context, units []*unit, timeout time.Duration, fn func(context.Context, *unit) (T, error)) []result[T] {
	results := make([]result[T], len(units))
	var wg sync.WaitGroup
	// A span per unit only inside a traced request: the reconciler's background passes
	// would otherwise flood Jaeger with root traces every 10 s.
	traced := trace.SpanFromContext(ctx).SpanContext().IsValid()
	for i, u := range units {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			var span trace.Span
			if traced {
				ctx, span = tracer.Start(ctx, "store "+u.name)
			}
			value, err := fn(ctx, u)
			if span != nil {
				if err != nil {
					span.SetStatus(codes.Error, err.Error())
				}
				span.End()
			}
			results[i] = result[T]{unit: u, value: value, err: err}
		})
	}
	wg.Wait()
	return results
}

// serves reports whether u may answer reads: its live incarnation is the one recorded
// in the map. A unit never reached yet is tried, and simply times out if down.
func (l *layout) serves(u *unit) bool {
	live := u.incarnation.Load()
	return live == nil || l.m.Incarnations[u.name] == *live
}

// readable lists units allowed to serve reads.
func (l *layout) readable(units []*unit) []*unit {
	return slices.DeleteFunc(slices.Clone(units), func(u *unit) bool { return !l.serves(u) })
}

// held lists the partitions a unit serves, as the int[] the unit queries expect.
func (l *layout) held(u *unit) []int32 {
	var parts []int32
	for p, replicas := range l.replicas {
		if slices.Contains(replicas, u) {
			parts = append(parts, int32(p))
		}
	}
	return parts
}

// readPlan is the outcome of one scatter.
type readPlan struct {
	partial     bool
	unavailable []string
}

// plan reports partial only when a partition has no answering replica.
func plan[T any](r *Router, l *layout, results []result[T]) readPlan {
	answered := map[*unit]bool{}
	pl := readPlan{unavailable: []string{}}
	for _, res := range results {
		if res.err == nil {
			answered[res.unit] = true
		}
	}
	for _, u := range r.units {
		if !answered[u] {
			pl.unavailable = append(pl.unavailable, u.name)
		}
	}
	for _, replicas := range l.replicas {
		pl.partial = pl.partial || !slices.ContainsFunc(replicas, func(u *unit) bool { return answered[u] })
	}
	return pl
}

func noUnitAnswered[T any](results []result[T]) error {
	errs := []error{errors.New("store: no unit answered")}
	for _, res := range results {
		if res.err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", res.unit.name, res.err))
	}
	return errors.Join(errs...)
}

// Fence raises the epoch on every reachable unit. An unreachable unit is fenced by the
// leader's first write to it (D-FENCE §9.3 limitation). A multi-unit deployment
// requires at least RF=2 acknowledgements before workers may start.
func (r *Router) Fence(ctx context.Context, epoch int64) error {
	results := gather(ctx, r.units, operationTimeout, func(ctx context.Context, u *unit) (struct{}, error) {
		return struct{}{}, u.repo.Fence(ctx, epoch)
	})
	for _, res := range results {
		if errors.Is(res.err, ErrFenced) {
			return ErrFenced
		}
		if res.err != nil {
			r.log(slog.LevelWarn, "fence skipped unreachable unit", "unit", res.unit.name, "epoch", epoch, "error", res.err)
		}
	}
	answered := 0
	for _, res := range results {
		if res.err == nil {
			answered++
		}
	}
	if answered < min(ReplicationFactor, len(r.units)) {
		return fmt.Errorf("store: fence acknowledged by %d units; need %d", answered, min(ReplicationFactor, len(r.units)))
	}
	return nil
}

// Upsert writes each event to every replica of its partition; an event counts as
// stored once one replica committed. Lagging replicas are caught up by anti-entropy.
func (r *Router) Upsert(ctx context.Context, epoch int64, events []domain.HazardEvent, correlationID string) ([]domain.HazardEvent, error) {
	return r.upsert(ctx, epoch, events, correlationID, false)
}

// UpsertReplicated is the ingestion acknowledgement barrier: advancing an upstream
// cursor requires all assigned replicas (including an in-progress move) to commit.
// Holding mapMu prevents a concurrent placement flip from discarding an acked copy.
func (r *Router) UpsertReplicated(ctx context.Context, epoch int64, events []domain.HazardEvent, correlationID string) ([]domain.HazardEvent, error) {
	r.mapMu.Lock()
	defer r.mapMu.Unlock()
	return r.upsert(ctx, epoch, events, correlationID, true)
}

func (r *Router) upsert(ctx context.Context, epoch int64, events []domain.HazardEvent, correlationID string, replicated bool) ([]domain.HazardEvent, error) {
	if err := verifyWrite(ctx); err != nil {
		return nil, err
	}
	l := r.current()
	batches := map[*unit][]domain.HazardEvent{}
	for _, event := range events {
		if replicated && len(l.writes[PartitionOf(event.HazardID)]) < min(ReplicationFactor, len(r.units)) {
			return nil, fmt.Errorf("store: partition %d is under-replicated", PartitionOf(event.HazardID))
		}
		for _, u := range l.writes[PartitionOf(event.HazardID)] {
			batches[u] = append(batches[u], event)
		}
	}
	targets := slices.DeleteFunc(slices.Clone(r.units), func(u *unit) bool { return len(batches[u]) == 0 })
	results := gather(ctx, targets, operationTimeout, func(ctx context.Context, u *unit) ([]domain.HazardEvent, error) {
		if err := verifyWrite(ctx); err != nil {
			return nil, err
		}
		return u.repo.Upsert(ctx, epoch, batches[u], correlationID)
	})
	stored := map[string]bool{}
	changed := map[string]domain.HazardEvent{}
	var failures []error
	for _, res := range results {
		if errors.Is(res.err, ErrFenced) {
			return nil, ErrFenced
		}
		if res.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", res.unit.name, res.err))
			r.log(slog.LevelWarn, "store replica write failed; replica now lags", "unit", res.unit.name,
				"correlation_id", correlationID, "events", len(batches[res.unit]), "error", res.err)
			continue
		}
		for _, event := range batches[res.unit] {
			stored[event.HazardID] = true
		}
		for _, event := range res.value {
			changed[event.HazardID] = event
		}
	}
	if replicated && len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	out := make([]domain.HazardEvent, 0, len(changed))
	for _, event := range events {
		if !stored[event.HazardID] {
			return nil, fmt.Errorf("partition %d has no replica that committed: %w",
				PartitionOf(event.HazardID), errors.Join(failures...))
		}
		if c, ok := changed[event.HazardID]; ok {
			out = append(out, c)
			delete(changed, event.HazardID)
		}
	}
	return out, nil
}

// List serves the M1 read API (occurred_at descending). Each unit returns its own
// top-limit, so the merged top-limit is exact.
func (r *Router) List(ctx context.Context, filter Filter, correlationID string) (events []domain.HazardEvent, err error) {
	started := time.Now()
	var pl readPlan
	defer func() {
		level := slog.LevelInfo
		if err != nil || len(pl.unavailable) > 0 {
			level = slog.LevelWarn
		}
		r.log(level, "canonical operation complete", "target", "canonical-store", "operation", "canonical-list",
			"correlation_id", correlationID, "latency_ms", float64(time.Since(started).Microseconds())/1000,
			"unavailable_units", pl.unavailable, "partial", pl.partial, "error", err)
	}()
	l := r.current()
	results := gather(ctx, l.readable(r.units), unitReadTimeout, func(ctx context.Context, u *unit) ([]domain.HazardEvent, error) {
		return u.repo.listParts(ctx, filter, l.held(u))
	})
	if err := noUnitAnswered(results); err != nil {
		return nil, err
	}
	pl = plan(r, l, results)
	events = merge(results, func(a, b domain.HazardEvent) int {
		return cmp.Or(b.OccurredAt.Compare(a.OccurredAt), strings.Compare(a.HazardID, b.HazardID))
	})
	return events[:min(len(events), clampLimit(filter.Limit))], nil
}

// Query pages in Audit API order (ingested_at, hazard_id) with an opaque cursor.
func (r *Router) Query(ctx context.Context, filter Filter, cursor string) (Page, error) {
	after, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	limit := clampLimit(filter.Limit)
	l := r.current()
	results := gather(ctx, l.readable(r.units), unitReadTimeout, func(ctx context.Context, u *unit) ([]domain.HazardEvent, error) {
		return u.repo.queryParts(ctx, filter, after, l.held(u), limit+1)
	})
	if err := noUnitAnswered(results); err != nil {
		return Page{}, err
	}
	pl := plan(r, l, results)
	events := merge(results, func(a, b domain.HazardEvent) int {
		return cmp.Or(a.IngestedAt.Compare(b.IngestedAt), strings.Compare(a.HazardID, b.HazardID))
	})
	page := Page{Items: events, Partial: pl.partial, UnavailableUnits: pl.unavailable}
	if len(events) > limit {
		page.Items = events[:limit]
		last := page.Items[limit-1]
		next := encodeCursor(pageCursor{IngestedAt: last.IngestedAt, HazardID: last.HazardID})
		page.NextCursor = &next
	}
	return page, nil
}

// merge unions both copies of every partition, so a replica that missed writes while
// down (no anti-entropy yet) cannot hide rows. Copies of one hazard keep the later
// ingested_at: ingest only moves it when the content changes.
func merge(results []result[[]domain.HazardEvent], compare func(a, b domain.HazardEvent) int) []domain.HazardEvent {
	newest := map[string]domain.HazardEvent{}
	for _, res := range results {
		for _, event := range res.value {
			if old, seen := newest[event.HazardID]; !seen || event.IngestedAt.After(old.IngestedAt) {
				newest[event.HazardID] = event
			}
		}
	}
	events := make([]domain.HazardEvent, 0, len(newest))
	for _, event := range newest {
		events = append(events, event)
	}
	slices.SortFunc(events, compare)
	return events
}

// Checksum follows kontrak-m2.md §7.3; an empty source covers every source.
func (r *Router) Checksum(ctx context.Context, source string) (ChecksumResult, error) {
	asOf := time.Now().UTC()
	l := r.current()
	results := gather(ctx, l.readable(r.units), unitReadTimeout, func(ctx context.Context, u *unit) ([]partRef, error) {
		return u.repo.refIDs(ctx, source, l.held(u))
	})
	if err := noUnitAnswered(results); err != nil {
		return ChecksumResult{}, err
	}
	pl := plan(r, l, results)
	refs := map[string]string{} // hazard_id -> source_ref_id, union of both copies.
	for _, res := range results {
		for _, ref := range res.value {
			refs[ref.hazardID] = ref.ref
		}
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref)
	}
	sum := checksumOf(source, ids, asOf)
	sum.Partial, sum.UnavailableUnits = pl.partial, pl.unavailable
	return sum, nil
}

// Analytics counts events with occurred_at in [from, to) into n buckets. Zero from/to
// mean the span of the data. Copies are unioned by hazard_id exactly like Checksum.
// shortcut: ships every row in range to the Aggregator; push GROUP BY down per partition
// if the catalog outgrows unitReadTimeout.
func (r *Router) Analytics(ctx context.Context, from, to time.Time, n int) (Analytics, error) {
	l := r.current()
	lo, hi := from, to
	if hi.IsZero() {
		hi = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	results := gather(ctx, l.readable(r.units), unitReadTimeout, func(ctx context.Context, u *unit) ([]analyticRow, error) {
		return u.repo.analyticRows(ctx, lo, hi, l.held(u))
	})
	if err := noUnitAnswered(results); err != nil {
		return Analytics{}, err
	}
	pl := plan(r, l, results)
	rows := map[string]analyticRow{}
	for _, res := range results {
		for _, row := range res.value {
			rows[row.hazardID] = row
		}
	}
	if from.IsZero() || to.IsZero() {
		var first, last time.Time
		for _, row := range rows {
			if first.IsZero() || row.occurred.Before(first) {
				first = row.occurred
			}
			if row.occurred.After(last) {
				last = row.occurred
			}
		}
		if from.IsZero() {
			lo = first
		}
		if to.IsZero() {
			hi = last.Add(time.Nanosecond)
		}
	}
	a := Analytics{From: lo.UTC(), To: hi.UTC(), Series: map[string][]int{}, BySeverity: map[string]int{},
		ByArea: map[string]int{}, Total: len(rows), Partial: pl.partial, UnavailableUnits: pl.unavailable}
	width := max(hi.Sub(lo)/time.Duration(n), time.Nanosecond)
	for i := range n {
		a.Buckets = append(a.Buckets, a.From.Add(time.Duration(i)*width))
	}
	for _, row := range rows {
		if a.Series[row.hazardType] == nil {
			a.Series[row.hazardType] = make([]int, n)
		}
		a.Series[row.hazardType][min(int(row.occurred.Sub(lo)/width), n-1)]++
		a.BySeverity[row.severity]++
		a.ByArea[row.area]++
	}
	return a, nil
}

// Locate asks every replica of the hazard's partition. No locations with partial=true
// means the answer is unknown (a replica that might hold it did not answer), not 404.
func (r *Router) Locate(ctx context.Context, hazardID string) ([]Location, bool, error) {
	type found struct {
		exists bool
		node   string
	}
	replicas := r.current().replicas[PartitionOf(hazardID)]
	results := gather(ctx, replicas, unitReadTimeout, func(ctx context.Context, u *unit) (found, error) {
		exists, node, err := u.repo.locate(ctx, hazardID)
		return found{exists, node}, err
	})
	if err := noUnitAnswered(results); err != nil {
		return nil, false, err
	}
	var locations []Location
	partial := false
	for i, res := range results {
		partial = partial || res.err != nil
		if res.err == nil && res.value.exists {
			role := "replica"
			if i == 0 {
				role = "primary"
			}
			locations = append(locations, Location{Node: res.value.node, Unit: res.unit.name, Role: role})
		}
	}
	return locations, partial, nil
}

// SaveCursor writes the cursor to every unit; one commit is enough because a stale
// copy only makes a later poll re-read data, which upserts idempotently.
func (r *Router) SaveCursor(ctx context.Context, epoch int64, source, cursor string) error {
	return r.saveCursor(ctx, epoch, source, cursor, 1)
}

// SaveCursorReplicated persists recovery state on at least RF units. A retry can
// complete a partial checkpoint, and older checkpoints only cause safe replay.
func (r *Router) SaveCursorReplicated(ctx context.Context, epoch int64, source, cursor string) error {
	return r.saveCursor(ctx, epoch, source, cursor, min(ReplicationFactor, len(r.units)))
}
func (r *Router) saveCursor(ctx context.Context, epoch int64, source, cursor string, required int) error {
	if err := verifyWrite(ctx); err != nil {
		return err
	}
	seq := r.cursorSeq.Add(1)
	results := gather(ctx, r.units, operationTimeout, func(ctx context.Context, u *unit) (struct{}, error) {
		return struct{}{}, u.repo.saveCursor(ctx, epoch, seq, source, cursor)
	})
	for _, res := range results {
		if errors.Is(res.err, ErrFenced) {
			return ErrFenced
		}
	}
	answered := 0
	for _, res := range results {
		if res.err == nil {
			answered++
		}
	}
	if answered < required {
		return fmt.Errorf("store: checkpoint acknowledged by %d units; need %d", answered, required)
	}
	return nil
}

// LoadCursor returns the copy with the highest (epoch, seq), or "" when none exists.
func (r *Router) LoadCursor(ctx context.Context, source string) (string, error) {
	type loaded struct {
		cursor savedCursor
		found  bool
	}
	l := r.current()
	results := gather(ctx, l.readable(r.units), unitReadTimeout, func(ctx context.Context, u *unit) (loaded, error) {
		c, found, err := u.repo.loadCursor(ctx, source)
		return loaded{c, found}, err
	})
	if err := noUnitAnswered(results); err != nil {
		return "", err
	}
	var best *savedCursor
	for _, res := range results {
		c := res.value.cursor
		if res.err == nil && res.value.found &&
			(best == nil || c.epoch > best.epoch || (c.epoch == best.epoch && c.seq > best.seq)) {
			best = &c
		}
	}
	if best == nil {
		return "", nil
	}
	return best.value, nil
}

// Ping succeeds while any unit answers; per-partition health shows up as partial reads.
func (r *Router) Ping(ctx context.Context) error {
	return noUnitAnswered(gather(ctx, r.units, operationTimeout, func(ctx context.Context, u *unit) (struct{}, error) {
		return struct{}{}, u.repo.Ping(ctx)
	}))
}

// PendingOutbox collects unpublished messages from every reachable unit. Each replica
// queues its own copy; consumers dedup on MessageId (hazard_id + payload hash).
func (r *Router) PendingOutbox(ctx context.Context) ([]OutboxMessage, error) {
	results := gather(ctx, r.units, operationTimeout, func(ctx context.Context, u *unit) ([]OutboxMessage, error) {
		return u.repo.PendingOutbox(ctx)
	})
	if err := noUnitAnswered(results); err != nil {
		return nil, err
	}
	var messages []OutboxMessage
	for _, res := range results {
		for _, message := range res.value {
			message.Unit = res.unit.name
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func (r *Router) MarkPublished(ctx context.Context, message OutboxMessage) error {
	u, ok := r.byName[message.Unit]
	if !ok {
		return fmt.Errorf("unknown store unit %q", message.Unit)
	}
	return u.repo.MarkPublished(ctx, message)
}
