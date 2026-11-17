package satellite

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func mutate(offset int64, d Detection) Mutation {
	return Mutation{Offset: offset, Kind: "detection", Detection: &d}
}
func oneID(t *testing.T, s *State) string {
	t.Helper()
	if len(s.Events) != 1 {
		t.Fatalf("events=%d", len(s.Events))
	}
	for id := range s.Events {
		return id
	}
	return ""
}

func TestStatePreservesIdentityAcrossUpdateRetractionAndReturn(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	a, b, c := detection("N20", "0000", "113.80000"), detection("N20", "0001", "113.80100"), detection("N21", "0002", "113.80200")
	original := NewState()
	state, _, err := original.Next([]Mutation{mutate(1, a), mutate(2, b), mutate(3, c)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if original.Offset != -1 || len(original.Detections) != 0 {
		t.Fatal("uncommitted state changed")
	}
	id := oneID(t, state)
	a.FRP = 100
	state, projection, err := state.Next([]Mutation{{Offset: 4, Kind: "update", Detection: &a}}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if oneID(t, state) != id || len(projection.Upserts) != 1 || state.Events[id].Severity != "AWAS" {
		t.Fatal("update changed identity or missed severity")
	}
	state, _, err = state.Next([]Mutation{{Offset: 5, Kind: "retract", Key: a.Key()}}, now.Add(2*time.Second))
	if err != nil || oneID(t, state) != id {
		t.Fatal("anchor retraction changed identity", err)
	}
	state, projection, err = state.Next([]Mutation{{Offset: 6, Kind: "retract", Key: b.Key()}}, now.Add(3*time.Second))
	if err != nil || len(state.Events) != 0 || state.Singles != 1 || len(projection.Retracted) != 1 || projection.Retracted[0].SourceRefID != id {
		t.Fatal("vanished fire was not withdrawn", err)
	}
	state, _, err = state.Next([]Mutation{mutate(7, b)}, now.Add(4*time.Second))
	if err != nil || oneID(t, state) != id {
		t.Fatal("return lost identity", err)
	}
}

func TestStateMergeSplitReplayAndBatchBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	points := []Detection{detection("N20", "0000", "113.80000"), detection("N20", "0001", "113.80100"), detection("N21", "0000", "113.81700"), detection("N21", "0001", "113.81800"), detection("N20", "0002", "113.80900")}
	batch := []Mutation{}
	for i, d := range points {
		batch = append(batch, mutate(int64(i+1), d))
	}
	batch = append(batch, Mutation{Offset: 6, Kind: "retract", Key: points[4].Key()})
	together, _, err := NewState().Next(batch, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(together.Events) != 2 {
		t.Fatalf("split produced %d fires", len(together.Events))
	}
	separately := NewState()
	for _, m := range batch {
		separately, _, err = separately.Next([]Mutation{m}, now)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(separately)
		if err != nil {
			t.Fatal(err)
		}
		var restored State
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		separately = &restored
	}
	a, _ := json.Marshal(together)
	b, _ := json.Marshal(separately)
	if string(a) != string(b) {
		t.Fatal("crash/batch boundary changed deterministic state")
	}
	replay, projection, err := separately.Next(batch, now.Add(time.Hour))
	if err != nil || !reflect.DeepEqual(projection, Projection{}) || replay.Offset != 6 {
		t.Fatal("replayed offsets created effects", err)
	}
	ids := map[string]bool{}
	for _, event := range together.Events {
		if ids[event.HazardID] {
			t.Fatal("split duplicated a hazard ID")
		}
		ids[event.HazardID] = true
	}
}

func TestStateRetainsAndCountsLateDetectionsOnce(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	fresh := detection("N20", "0", "113.80000")
	fresh.AcqDate = "2026-11-15"
	old := detection("N20", "0", "114.00000")
	s, _, err := NewState().Next([]Mutation{mutate(1, fresh), mutate(2, old), mutate(3, old)}, now)
	if err != nil || s.LateDetections != 1 || len(s.Detections) != 2 {
		t.Fatalf("late=%d detections=%d err=%v", s.LateDetections, len(s.Detections), err)
	}
	if _, _, err := s.Next([]Mutation{mutate(5, old)}, now); err == nil || s.Offset != 3 {
		t.Fatal("gap advanced committed state")
	}
	if _, _, err := s.Next([]Mutation{{Offset: 4, Kind: "update"}}, now); err == nil || s.Offset != 3 {
		t.Fatal("invalid payload advanced state")
	}
}

func TestProvinceReferenceAndHazardMapping(t *testing.T) {
	if len(provinceReference.Provinces) != 38 {
		t.Fatal("expected 38 province centres")
	}
	names := map[string]bool{}
	for _, p := range provinceReference.Provinces {
		if names[p.Name] || nearestProvince(p.Latitude, p.Longitude) != p.Name {
			t.Fatalf("invalid reference %s", p.Name)
		}
		names[p.Name] = true
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	fires, _, err := Cluster([]Detection{detection("N20", "243", "113.80000"), detection("N21", "244", "113.80100")})
	if err != nil || len(fires) != 1 {
		t.Fatal(err)
	}
	event := fires[0].Hazard(now)
	if event.Source != "SATELIT" || event.HazardType != "WILDFIRE" || event.AreaName == "" || len(event.Attributes) != 5 || event.Attributes["detection_count"] != 2 || !event.IngestedAt.Equal(now) {
		t.Fatalf("%+v", event)
	}
}

func BenchmarkStateThousandDetections(b *testing.B) {
	batch := make([]Mutation, 1000)
	for i := range batch {
		d := detection("N20", "243", fmt.Sprintf("%.6f", 100+float64(i)*.03))
		batch[i] = mutate(int64(i+1), d)
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < b.N; i++ {
		if _, _, err := NewState().Next(batch, now); err != nil {
			b.Fatal(err)
		}
	}
}
