package satellite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

// DefaultLateness is a safe virtual/event-time default. Measured on the first
// 178k practice offsets: detection disorder max 4.5 h, update/retract age max 5.1 h.
// Late detections that can still join a retained fire are kept and counted;
// older ones are counted and ignored (see lateHorizon).
const DefaultLateness = 24 * time.Hour

// clusterWindow is the event-time adjacency limit used by Cluster and the grid.
const clusterWindow = 6 * time.Hour

// Mutation represents one ordered change to the satellite checkpoint.
type Mutation struct {
	Offset    int64
	Kind      string
	Detection *Detection
	Changes   *detectionChanges
	Key       string
}

// State is the recovery checkpoint. Owners retains identity when a fire becomes
// a singleton/disappears, and after merges/splits, without relying on process RAM.
type State struct {
	Offset         int64
	Lateness       time.Duration
	MaxEventTime   time.Time
	LastIngested   time.Time
	LateDetections uint64
	Singles        int
	Detections     map[string]Detection
	Owners         map[string]string
	Seen           map[string]bool
	Events         map[string]domain.HazardEvent
	// PrunedBefore is the hour-aligned horizon below which finalized fires and
	// detections were dropped. It depends only on the offset sequence, so crash and
	// batch boundaries cannot change what is pruned.
	PrunedBefore time.Time
}

type Projection struct {
	Upserts   []domain.HazardEvent
	Retracted []domain.HazardEvent
}

func NewState() *State {
	return NewStateWithLateness(DefaultLateness)
}

func NewStateWithLateness(lateness time.Duration) *State {
	if lateness <= 0 {
		lateness = DefaultLateness
	}
	return &State{Offset: -1, Lateness: lateness, Detections: map[string]Detection{}, Owners: map[string]string{}, Seen: map[string]bool{}, Events: map[string]domain.HazardEvent{}}
}
func (s *State) Watermark() time.Time {
	if s.MaxEventTime.IsZero() {
		return time.Time{}
	}
	return s.MaxEventTime.Add(-s.Lateness)
}

// lateHorizon is the oldest event time that can still change a fire: anything
// older is beyond the lateness bound by more than one cluster window.
func (s *State) lateHorizon() time.Time {
	w := s.Watermark()
	if w.IsZero() {
		return w
	}
	return w.Add(-clusterWindow)
}

// pruneHorizon keeps one more cluster window, so a detection accepted at
// lateHorizon can only link to retained detections and fire membership stays exact.
func (s *State) pruneHorizon() time.Time {
	h := s.lateHorizon()
	if h.IsZero() {
		return h
	}
	return h.Add(-clusterWindow).Truncate(time.Hour)
}

// keyTime recovers a detection's event time from its key (see Detection.Key).
func keyTime(key string) (time.Time, bool) {
	parts := strings.Split(key, "|")
	if len(parts) != 5 {
		return time.Time{}, false
	}
	p, err := parse(Detection{Satellite: parts[0], AcqDate: parts[1], AcqTime: parts[2], Latitude: parts[3], Longitude: parts[4], Confidence: "n"})
	return p.at, err == nil
}

// tooLate reports whether an unknown key is older than anything that can still change.
func (s *State) tooLate(key string) bool {
	h := s.lateHorizon()
	at, ok := keyTime(key)
	return ok && !h.IsZero() && at.Before(h)
}

// prune drops fires whose every detection is older than before, plus older
// singles, low-confidence detections and identity/retraction ledger entries.
// Dropped fires are already materialized; they are frozen, not retracted.
func (s *State) prune(before time.Time, grid detectionGrid, pruned map[string]domain.HazardEvent) {
	inFire := map[string]bool{}
	for id, event := range s.Events {
		keys, _ := eventKeys(event)
		final := true
		for _, key := range keys {
			inFire[key] = true
			if p, ok := grid.points[key]; !ok || !p.at.Before(before) {
				final = false
			}
		}
		if !final {
			continue
		}
		pruned[id] = event
		delete(s.Events, id)
		for _, key := range keys {
			grid.remove(key, grid.points[key])
			delete(grid.active, key)
			delete(s.Detections, key)
			delete(s.Owners, key)
			delete(s.Seen, key)
		}
	}
	for key, d := range s.Detections {
		if inFire[key] {
			continue
		}
		p, inGrid := grid.points[key]
		if !inGrid {
			var err error
			if p, err = parse(d); err != nil {
				continue
			}
		}
		if !p.at.Before(before) {
			continue
		}
		if inGrid {
			grid.remove(key, p)
			s.Singles--
		}
		delete(s.Detections, key)
		delete(s.Owners, key)
		delete(s.Seen, key)
	}
	// Retracted keys keep Seen/Owners entries; drop those that are equally old.
	stale := func(key string) bool {
		_, live := s.Detections[key]
		at, ok := keyTime(key)
		return !live && ok && at.Before(before)
	}
	for key := range s.Seen {
		if stale(key) {
			delete(s.Seen, key)
		}
	}
	for key := range s.Owners {
		if stale(key) {
			delete(s.Owners, key)
		}
	}
}

// applyMutation changes only the private batch state. The grid allows an isolated
// detection or heartbeat to advance the offset without reclustering old data.
func (next *State) applyMutation(mutation Mutation, ingested time.Time, grid detectionGrid) error {
	if mutation.Offset < 0 {
		return errors.New("satellite: negative offset")
	}
	if mutation.Offset <= next.Offset {
		return nil
	}
	if next.Offset >= 0 && mutation.Offset != next.Offset+1 {
		return fmt.Errorf("satellite: mutation gap after %d", next.Offset)
	}
	ingested = ingested.Truncate(time.Microsecond)
	if !ingested.After(next.LastIngested) {
		ingested = next.LastIngested.Truncate(time.Microsecond).Add(time.Microsecond)
	}
	next.LastIngested = ingested
	next.Offset = mutation.Offset
	affected := map[string]bool{}
	oldFires := map[string]domain.HazardEvent{}
	touchFire := func(id string) {
		if _, alreadyTouched := oldFires[id]; alreadyTouched {
			return
		}
		if event, exists := next.Events[id]; exists {
			oldFires[id] = event
			for _, key := range event.Attributes["detection_keys"].([]string) {
				affected[key] = true
			}
		}
	}
	touch := func(key string) {
		affected[key] = true
		touchFire(grid.active[key])
		touchFire(next.Owners[key])
	}
	remove := func(key string) {
		if old, found := grid.points[key]; found {
			if id := grid.active[key]; id != "" {
				touchFire(id)
			} else {
				next.Singles--
			}
			grid.remove(key, old)
		}
		delete(next.Detections, key)
	}
	addedKey := ""
	switch mutation.Kind {
	case "detection", "update":
		if mutation.Changes != nil {
			previous, exists := next.Detections[mutation.Key]
			if !exists {
				if next.Seen[mutation.Key] {
					return nil // A patch cannot restore a retracted detection.
				}
				if next.tooLate(mutation.Key) {
					next.LateDetections++ // its fire was finalized and pruned
					return nil
				}
				return fmt.Errorf("%w: patch for unseen detection at offset %d", ErrProtocol, mutation.Offset)
			}
			if mutation.Changes.Confidence != nil {
				previous.Confidence = *mutation.Changes.Confidence
			}
			if mutation.Changes.FRP != nil {
				previous.FRP = *mutation.Changes.FRP
			}
			if previous == next.Detections[mutation.Key] {
				return nil
			}
			mutation.Detection = &previous
		}
		if mutation.Detection == nil {
			return errors.New("satellite: detection required")
		}
		p, err := parse(*mutation.Detection)
		if err != nil {
			return fmt.Errorf("%w: %v at offset %d", ErrProtocol, err, mutation.Offset)
		}
		key := mutation.Detection.Key()
		_, exists := next.Detections[key]
		if mutation.Kind == "detection" && exists {
			return nil
		}
		if h := next.lateHorizon(); !exists && !next.Seen[key] && !h.IsZero() && p.at.Before(h) {
			next.LateDetections++ // too old to join any retained fire; never revive pruned state
			return nil
		}
		if !next.Seen[key] && !next.Watermark().IsZero() && p.at.Before(next.Watermark()) {
			next.LateDetections++
		}
		if p.at.After(next.MaxEventTime) {
			next.MaxEventTime = p.at
		}
		if mutation.Key != "" && mutation.Key != key {
			if owner := next.Owners[mutation.Key]; owner != "" {
				next.Owners[key] = owner
			}
			remove(mutation.Key)
		}
		remove(key)
		next.Detections[key] = *mutation.Detection
		next.Seen[key] = true
		if p.confidence != "l" {
			addedKey = key
			grid.neighbors(key, p, touch)
			touch(key)
			grid.add(key, p)
		}
	case "heartbeat":
		return nil
	case "retract":
		if mutation.Key == "" {
			return errors.New("satellite: retraction key required")
		}
		if _, exists := next.Detections[mutation.Key]; !exists && !next.Seen[mutation.Key] && next.tooLate(mutation.Key) {
			next.LateDetections++
			return nil
		}
		remove(mutation.Key)
	default:
		return fmt.Errorf("satellite: unsupported mutation %q", mutation.Kind)
	}
	input := make([]Detection, 0, len(affected))
	for key := range affected {
		if p, exists := grid.points[key]; exists {
			input = append(input, p.d)
			if grid.active[key] == "" && key != addedKey {
				next.Singles--
			}
		}
		delete(grid.active, key)
	}
	fires, singles, err := Cluster(input)
	if err != nil {
		return err
	}
	next.Singles += singles
	// Each old identity nominates one successor, favouring the largest overlap.
	// Ties use the component's smallest key. A merge keeps the smallest nominated ID.
	sort.Slice(fires, func(i, j int) bool { return fires[i].DetectionKeys[0] < fires[j].DetectionKeys[0] })
	type choice struct{ component, count int }
	nominations := map[string]choice{}
	for i, fire := range fires {
		counts := map[string]int{}
		for _, key := range fire.DetectionKeys {
			if owner := next.Owners[key]; owner != "" {
				counts[owner]++
			}
		}
		for owner, count := range counts {
			old, ok := nominations[owner]
			if !ok || count > old.count {
				nominations[owner] = choice{i, count}
			}
		}
	}
	candidates := make([][]string, len(fires))
	for owner, choice := range nominations {
		candidates[choice.component] = append(candidates[choice.component], owner)
	}
	for id := range oldFires {
		delete(next.Events, id)
	}
	for i, fire := range fires {
		if len(candidates[i]) > 0 {
			sort.Strings(candidates[i])
			fire.SourceRefID = candidates[i][0]
		}
		// A split child whose smallest key was the old anchor must not collide with
		// the ID retained by the larger successor. Salt deterministically if needed.
		_, inUse := next.Events[fire.SourceRefID]
		if _, reserved := nominations[fire.SourceRefID]; (reserved || inUse) && len(candidates[i]) == 0 {
			fire.SourceRefID += "-split-" + fmt.Sprint(next.Offset)
		}
		event := fire.Hazard(ingested)
		previous, exists := oldFires[fire.SourceRefID]
		if exists {
			comparable := event
			comparable.IngestedAt = previous.IngestedAt
			a, _ := json.Marshal(comparable)
			b, _ := json.Marshal(previous)
			if bytes.Equal(a, b) {
				event = previous
			}
		}
		next.Events[fire.SourceRefID] = event
		for _, key := range fire.DetectionKeys {
			next.Owners[key] = fire.SourceRefID
			grid.active[key] = fire.SourceRefID
		}
	}
	return nil
}

// Apply every mutation in offset order even when the caller batches commits, so
// a different crash/reconnect boundary cannot change merge/split identity choices.
func (s *State) Next(batch []Mutation, ingested time.Time) (*State, Projection, error) {
	if len(batch) == 0 {
		return s, Projection{}, nil
	}
	if s.Lateness <= 0 || s.Offset < -1 || s.Detections == nil || s.Owners == nil || s.Events == nil {
		return nil, Projection{}, errors.New("satellite: invalid checkpoint")
	}
	if ingested.IsZero() {
		return nil, Projection{}, errors.New("satellite: ingestion time required")
	}
	next := *s
	next.Detections, next.Owners, next.Seen = maps.Clone(s.Detections), maps.Clone(s.Owners), maps.Clone(s.Seen)
	next.Events = maps.Clone(s.Events)
	if next.Seen == nil {
		next.Seen = map[string]bool{}
	}
	grid := detectionGrid{points: map[string]point{}, cells: map[[4]int64]map[string]point{}, active: map[string]string{}}
	for id, event := range next.Events {
		keys, err := eventKeys(event)
		if err != nil {
			return nil, Projection{}, err
		}
		if _, ok := event.Attributes["detection_keys"].([]string); !ok {
			event.Attributes = maps.Clone(event.Attributes)
			event.Attributes["detection_keys"] = keys
			next.Events[id] = event
		}
		for _, key := range keys {
			grid.active[key] = id
		}
	}
	for key, detection := range next.Detections {
		p, err := parse(detection)
		if err != nil {
			return nil, Projection{}, err
		}
		if p.confidence != "l" {
			grid.add(key, p)
		}
	}
	pruned := map[string]domain.HazardEvent{}
	for _, mutation := range batch {
		if err := next.applyMutation(mutation, ingested, grid); err != nil {
			return nil, Projection{}, err
		}
		// Checked per mutation, not per batch, so the result is batch-boundary independent.
		if h := next.pruneHorizon(); h.After(next.PrunedBefore) {
			next.prune(h, grid, pruned)
			next.PrunedBefore = h
		}
	}
	if next.Offset == s.Offset {
		return s, Projection{}, nil
	}
	projection := Projection{}
	changed := func(id string, event domain.HazardEvent) bool {
		before, exists := s.Events[id]
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(event)
		return !exists || !bytes.Equal(a, b)
	}
	for id, event := range next.Events {
		if changed(id, event) {
			projection.Upserts = append(projection.Upserts, event)
		}
	}
	// A pruned fire is frozen in its last state, never withdrawn.
	for id, event := range pruned {
		if _, live := next.Events[id]; !live && changed(id, event) {
			projection.Upserts = append(projection.Upserts, event)
		}
	}
	for id, event := range s.Events {
		_, exists := next.Events[id]
		if _, frozen := pruned[id]; !exists && !frozen {
			projection.Retracted = append(projection.Retracted, event)
		}
	}
	sort.Slice(projection.Upserts, func(i, j int) bool { return projection.Upserts[i].HazardID < projection.Upserts[j].HazardID })
	sort.Slice(projection.Retracted, func(i, j int) bool { return projection.Retracted[i].HazardID < projection.Retracted[j].HazardID })
	return &next, projection, nil
}

// JSON checkpoints restore attribute arrays as []any.
func eventKeys(event domain.HazardEvent) ([]string, error) {
	switch keys := event.Attributes["detection_keys"].(type) {
	case []string:
		return keys, nil
	case []any:
		result := make([]string, 0, len(keys))
		for _, value := range keys {
			key, ok := value.(string)
			if !ok {
				return nil, errors.New("satellite: invalid checkpoint detection key")
			}
			result = append(result, key)
		}
		return result, nil
	default:
		return nil, errors.New("satellite: checkpoint fire is missing detection keys")
	}
}
