// AI assistance is disclosed in docs/m2/ai-usage.md; human review is required.
package satellite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

// DefaultLateness is provisional virtual/event time, not 24 hours of wall time.
// All over-limit detections are retained and counted; observation may tune it.
const DefaultLateness = 24 * time.Hour

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
}

type Projection struct {
	Upserts   []domain.HazardEvent
	Retracted []domain.HazardEvent
}

func NewState() *State {
	return &State{Offset: -1, Lateness: DefaultLateness, Detections: map[string]Detection{}, Owners: map[string]string{}, Seen: map[string]bool{}, Events: map[string]domain.HazardEvent{}}
}
func (s *State) Watermark() time.Time {
	if s.MaxEventTime.IsZero() {
		return time.Time{}
	}
	return s.MaxEventTime.Add(-s.Lateness)
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
		if _, exists := next.Detections[key]; mutation.Kind == "detection" && exists {
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
	for _, mutation := range batch {
		if err := next.applyMutation(mutation, ingested, grid); err != nil {
			return nil, Projection{}, err
		}
	}
	if next.Offset == s.Offset {
		return s, Projection{}, nil
	}
	projection := Projection{}
	for id, event := range next.Events {
		before, exists := s.Events[id]
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(event)
		if !exists || !bytes.Equal(a, b) {
			projection.Upserts = append(projection.Upserts, event)
		}
	}
	for id, event := range s.Events {
		if _, exists := next.Events[id]; !exists {
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
