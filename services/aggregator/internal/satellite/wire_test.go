package satellite

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestPracticeFeedPatches(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	const a = "N20|2026-11-12|11|-4.93869|137.96748"
	frames := []Frame{
		{Kind: "detection", Offset: 0, Data: []byte(`{"offset":0,"detection":{"satellite":"N20","acq_date":"2026-11-12","acq_time":11,"latitude":-4.93869,"longitude":137.96748,"confidence":"n","frp":3}}`)},
		{Kind: "detection", Offset: 1, Data: []byte(`{"offset":1,"detection":{"satellite":"N","acq_date":"2026-11-12","acq_time":16,"latitude":-4.93394,"longitude":137.96901,"confidence":"h","frp":4}}`)},
		{Kind: "update", Offset: 2, Data: []byte(`{"offset":2,"key":"` + a + `","changes":{"bright_ti5":281.74}}`)},
		{Kind: "update", Offset: 3, Data: []byte(`{"offset":3,"key":"` + a + `","changes":{"frp":9.43}}`)},
		{Kind: "retract", Offset: 4, Data: []byte(`{"offset":4,"reason":"false_positive","key":"` + a + `"}`)},
	}
	state := NewState()
	var id string
	for _, frame := range frames {
		mutation, err := decodeFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		next, projection, err := state.Next([]Mutation{mutation}, now)
		if err != nil || next.Offset != frame.Offset {
			t.Fatalf("did not apply offset %d: %v", frame.Offset, err)
		}
		switch frame.Offset {
		case 1:
			id = oneID(t, next)
		case 2:
			if len(projection.Upserts) != 0 || oneID(t, next) != id {
				t.Fatal("brightness change altered the canonical fire")
			}
		case 3:
			if len(projection.Upserts) != 1 || oneID(t, next) != id || next.Detections[a].FRP != 9.43 {
				t.Fatal("FRP patch did not update the existing fire")
			}
		case 4:
			if len(projection.Retracted) != 1 || len(next.Events) != 0 {
				t.Fatal("fire was not withdrawn")
			}
		}
		state = next
	}
}

func TestPracticeFeedRejectsOffsetMismatch(t *testing.T) {
	_, err := decodeFrame(Frame{Kind: "retract", Offset: 10, Data: []byte(`{"offset":11,"key":"N20|2026-11-12|11|-4.93869|137.96748"}`)})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("wanted protocol error, got %v", err)
	}
}

// Captured at practice offsets 162175 and 162263.
func TestExponentCoordinateMatchesFeedUpdateKey(t *testing.T) {
	const rawKey = "N20|2026-11-25|1704|-2.1e-4|110.82748"
	const feedKey = "N20|2026-11-25|1704|-0.00021|110.82748"
	now := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)
	state := NewState()
	apply := func(frame Frame) Projection {
		t.Helper()
		mutation, err := decodeFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		next, projection, err := state.Next([]Mutation{mutation}, now)
		if err != nil {
			t.Fatal(err)
		}
		state = next
		return projection
	}
	apply(Frame{Kind: "detection", Offset: 0, Data: []byte(`{"detection":{"satellite":"N20","acq_date":"2026-11-25","acq_time":1704,"latitude":-2.1e-4,"longitude":110.82748,"confidence":"n","frp":3.66}}`)})
	fire := apply(Frame{Kind: "detection", Offset: 1, Data: []byte(`{"detection":{"satellite":"N21","acq_date":"2026-11-25","acq_time":1704,"latitude":0.00010,"longitude":110.82748,"confidence":"n","frp":4}}`)})
	if len(fire.Upserts) != 1 {
		t.Fatalf("expected one fire, got %+v", fire)
	}
	checkpoint, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	state = new(State)
	if err := json.Unmarshal(checkpoint, state); err != nil {
		t.Fatal(err)
	}
	revised := apply(Frame{Kind: "update", Offset: 2, Data: []byte(`{"key":"` + feedKey + `","changes":{"frp":30}}`)})
	if len(revised.Upserts) != 1 || revised.Upserts[0].HazardID != fire.Upserts[0].HazardID || revised.Upserts[0].Severity != "SIAGA" {
		t.Fatalf("patch did not revise the existing fire: %+v", revised)
	}
	withdrawn := apply(Frame{Kind: "retract", Offset: 3, Data: []byte(`{"key":"` + feedKey + `"}`)})
	if len(withdrawn.Retracted) != 1 || withdrawn.Retracted[0].HazardID != fire.Upserts[0].HazardID {
		t.Fatalf("retract did not withdraw the existing fire: %+v", withdrawn)
	}
	for _, projection := range []Projection{fire, revised} {
		keys, err := eventKeys(projection.Upserts[0])
		if err != nil || !slices.Equal(keys, []string{rawKey, "N21|2026-11-25|1704|0.00010|110.82748"}) {
			t.Fatalf("projected detection keys do not preserve the raw feed: %v, %v", keys, err)
		}
	}
}
