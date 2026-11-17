// AI assistance is disclosed in docs/m2/ai-usage.md; human review is required.
package satellite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

// DefaultLateness is provisional virtual/event time, not 24 hours of wall time.
// All over-limit detections are retained and counted; observation may tune it.
const DefaultLateness = 24 * time.Hour

// Mutation is an internal adapter contract, not the assistant's unknown JSON schema.
type Mutation struct {
	Offset    int64
	Kind      string
	Detection *Detection
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

// applyMutation is copy-on-write: a failed batch leaves both the prior state and its
// committed offset untouched. The caller must durably persist the returned state
// and finish materializing its projection before accepting another batch.
func (s *State) applyMutation(mutation Mutation, ingested time.Time) (*State, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	next := NewState()
	if err := json.Unmarshal(raw, next); err != nil {
		return nil, err
	}
	if next.Lateness <= 0 || next.Offset < -1 || next.Detections == nil || next.Owners == nil || next.Events == nil {
		return nil, errors.New("satellite: invalid checkpoint")
	}
	if next.Seen == nil {
		next.Seen = map[string]bool{}
	}
	if ingested.IsZero() {
		return nil, errors.New("satellite: ingestion time required")
	}

	if mutation.Offset < 0 {
		return nil, errors.New("satellite: negative offset")
	}
	if mutation.Offset <= next.Offset {
		return next, nil
	}
	if next.Offset >= 0 && mutation.Offset != next.Offset+1 {
		return nil, fmt.Errorf("satellite: mutation gap after %d", next.Offset)
	}
	switch mutation.Kind {
	case "detection", "update":
		if mutation.Detection == nil {
			return nil, errors.New("satellite: detection required")
		}
		p, err := parse(*mutation.Detection)
		if err != nil {
			return nil, err
		}
		if !next.Seen[mutation.Detection.Key()] && !next.Watermark().IsZero() && p.at.Before(next.Watermark()) {
			next.LateDetections++
		}
		if p.at.After(next.MaxEventTime) {
			next.MaxEventTime = p.at
		}
		key := mutation.Detection.Key()
		if mutation.Key != "" && mutation.Key != key {
			if owner := next.Owners[mutation.Key]; owner != "" {
				next.Owners[key] = owner
			}
			delete(next.Detections, mutation.Key)
		}
		next.Detections[key] = *mutation.Detection
		next.Seen[key] = true
	case "retract":
		if mutation.Key == "" {
			return nil, errors.New("satellite: retraction key required")
		}
		delete(next.Detections, mutation.Key)
	default:
		return nil, fmt.Errorf("satellite: unsupported mutation %q", mutation.Kind)
	}
	if !ingested.After(next.LastIngested) {
		ingested = next.LastIngested.Add(time.Microsecond)
	}
	next.LastIngested = ingested
	next.Offset = mutation.Offset
	input := make([]Detection, 0, len(next.Detections))
	for _, d := range next.Detections {
		input = append(input, d)
	}
	fires, singles, err := Cluster(input)
	if err != nil {
		return nil, err
	}
	next.Singles = singles
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
	active := map[string]domain.HazardEvent{}
	for i, fire := range fires {
		if len(candidates[i]) > 0 {
			sort.Strings(candidates[i])
			fire.SourceRefID = candidates[i][0]
		}
		// A split child whose smallest key was the old anchor must not collide with
		// the ID retained by the larger successor. Salt deterministically if needed.
		if _, reserved := nominations[fire.SourceRefID]; reserved && len(candidates[i]) == 0 {
			fire.SourceRefID += "-split-" + fmt.Sprint(next.Offset)
		}
		event := fire.Hazard(ingested)
		previous, exists := next.Events[fire.SourceRefID]
		if exists {
			comparable := event
			comparable.IngestedAt = previous.IngestedAt
			a, _ := json.Marshal(comparable)
			b, _ := json.Marshal(previous)
			if bytes.Equal(a, b) {
				event = previous
			}
		}
		active[fire.SourceRefID] = event
		for _, key := range fire.DetectionKeys {
			next.Owners[key] = fire.SourceRefID
		}
	}
	next.Events = active
	return next, nil
}

// Apply every mutation in offset order even when the caller batches commits, so
// a different crash/reconnect boundary cannot change merge/split identity choices.
func (s *State) Next(batch []Mutation, ingested time.Time) (*State, Projection, error) {
	next := s
	for _, mutation := range batch {
		updated, err := next.applyMutation(mutation, ingested)
		if err != nil {
			return nil, Projection{}, err
		}
		next = updated
	}
	if next == s {
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
	return next, projection, nil
}
