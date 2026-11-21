package satellite

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
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
	// Watermark is 11-14 00:00; 11-13 20:00 is late but can still join a fire.
	old := detection("N20", "2000", "114.00000")
	old.AcqDate = "2026-11-13"
	// Older than one cluster window past the watermark: counted, not retained.
	ancient := detection("N20", "0", "114.50000")
	s, _, err := NewState().Next([]Mutation{mutate(1, fresh), mutate(2, old), mutate(3, old), mutate(4, ancient)}, now)
	if err != nil || s.LateDetections != 2 || len(s.Detections) != 2 {
		t.Fatalf("late=%d detections=%d err=%v", s.LateDetections, len(s.Detections), err)
	}
	if _, _, err := s.Next([]Mutation{{Offset: 5, Kind: "update", Key: ancient.Key(), Changes: &detectionChanges{}}}, now); err != nil {
		t.Fatal("patch for a detection beyond the horizon must be counted, not a protocol error", err)
	}
	if _, _, err := s.Next([]Mutation{mutate(6, old)}, now); err == nil || s.Offset != 4 {
		t.Fatal("gap advanced committed state")
	}
	if _, _, err := s.Next([]Mutation{{Offset: 5, Kind: "update"}}, now); err == nil || s.Offset != 4 {
		t.Fatal("invalid payload advanced state")
	}
}

func TestPruningFreezesFinishedFiresDeterministically(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	a, b := detection("N20", "0", "113.80000"), detection("N21", "1", "113.80100")
	later := func(clock, lon string) Detection {
		d := detection("N20", clock, lon)
		d.AcqDate = "2026-11-13"
		return d
	}
	// With 1 h lateness, an event at 11-13 20:00 prunes fires finished before 07:00.
	batch := []Mutation{mutate(1, a), mutate(2, b), mutate(3, later("1000", "120.00000")), mutate(4, later("1001", "120.00100")), mutate(5, later("2000", "125.00000"))}
	together, projection, err := NewStateWithLateness(time.Hour).Next(batch, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(together.Events) != 1 || len(projection.Retracted) != 0 || len(projection.Upserts) != 2 {
		t.Fatalf("events=%d upserts=%d retracted=%d", len(together.Events), len(projection.Upserts), len(projection.Retracted))
	}
	for _, key := range []string{a.Key(), b.Key()} {
		if _, ok := together.Detections[key]; ok || together.Seen[key] || together.Owners[key] != "" {
			t.Fatal("finished fire was not pruned", key)
		}
	}
	if len(together.Detections) != 3 || together.Singles != 1 || !together.PrunedBefore.Equal(time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("detections=%d singles=%d prunedBefore=%s", len(together.Detections), together.Singles, together.PrunedBefore)
	}

	separately := NewStateWithLateness(time.Hour)
	for _, m := range batch {
		if separately, _, err = separately.Next([]Mutation{m}, now); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(separately)
		var restored State
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		separately = &restored
	}
	x, _ := json.Marshal(together)
	y, _ := json.Marshal(separately)
	if string(x) != string(y) {
		t.Fatal("pruning depends on batch boundaries")
	}

	// A redelivered detection of the frozen fire is counted, never revived.
	again, projection, err := together.Next([]Mutation{mutate(6, a)}, now)
	if err != nil || len(again.Events) != 1 || len(projection.Upserts) != 0 || again.LateDetections != together.LateDetections+1 {
		t.Fatalf("revived pruned fire: events=%d upserts=%d err=%v", len(again.Events), len(projection.Upserts), err)
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

func TestHeartbeatOffsetsDoNotCreateDetections(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	state, projection, err := NewState().Next([]Mutation{{Offset: 1, Kind: "heartbeat"}, mutate(2, detection("N20", "243", "113.80000"))}, now)
	if err != nil || state.Offset != 2 || len(state.Detections) != 1 || len(state.Events) != 0 || len(projection.Upserts) != 0 {
		t.Fatalf("state=%+v projection=%+v err=%v", state, projection, err)
	}
}

func TestIncrementalClustersMatchFullRebuild(t *testing.T) {
	random := rand.New(rand.NewSource(49))
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	state := NewState()
	pool := make([]Detection, 50)
	for i := range pool {
		pool[i] = detection("N20", fmt.Sprint(100+i), fmt.Sprintf("%.5f", 113.8+float64(i%15)*.003))
	}
	for i := range 300 {
		point := pool[random.Intn(len(pool))]
		mutation := mutate(int64(i), point)
		switch random.Intn(3) {
		case 1:
			mutation.Kind, mutation.Key = "retract", point.Key()
		case 2:
			point.Confidence = []string{"l", "n", "h"}[random.Intn(3)]
			mutation.Kind, mutation.Detection = "update", &point
		}
		next, _, err := state.Next([]Mutation{mutation}, now)
		if err != nil {
			t.Fatal(err)
		}
		state = next
		input := make([]Detection, 0, len(state.Detections))
		for _, d := range state.Detections {
			input = append(input, d)
		}
		fires, singles, err := Cluster(input)
		if err != nil || singles != state.Singles || len(fires) != len(state.Events) {
			t.Fatalf("offset %d: full=%d/%d incremental=%d/%d err=%v", i, len(fires), singles, len(state.Events), state.Singles, err)
		}
		expected := map[string]bool{}
		for _, fire := range fires {
			expected[strings.Join(fire.DetectionKeys, ",")] = true
		}
		for _, event := range state.Events {
			keys, err := eventKeys(event)
			if err != nil || !expected[strings.Join(keys, ",")] {
				t.Fatalf("offset %d: detection membership differs from full rebuild", i)
			}
		}
	}
}

func BenchmarkStateClusteredThousandDetections(b *testing.B) {
	batch := make([]Mutation, 1000)
	for i := range batch {
		// 50 independent groups of 20 detections each.
		d := detection("N20", "243", fmt.Sprintf("%.6f", 100+float64(i/20)*.03+float64(i%20)*.00002))
		batch[i] = mutate(int64(i+1), d)
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < b.N; i++ {
		if _, _, err := NewState().Next(batch, now); err != nil {
			b.Fatal(err)
		}
	}
}
