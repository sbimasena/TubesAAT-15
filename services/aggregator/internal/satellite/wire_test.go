package satellite

import (
	"errors"
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
	detection, err := decodeFrame(Frame{Kind: "detection", Offset: 0, Data: []byte(`{"offset":0,"detection":{"satellite":"N20","acq_date":"2026-11-25","acq_time":1704,"latitude":-2.1e-4,"longitude":110.82748,"confidence":"n","frp":3.66}}`)})
	if err != nil {
		t.Fatal(err)
	}
	update, err := decodeFrame(Frame{Kind: "update", Offset: 1, Data: []byte(`{"offset":1,"key":"N20|2026-11-25|1704|-0.00021|110.82748","changes":{"frp":3.27}}`)})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := NewState().Next([]Mutation{detection, update}, time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC))
	if err != nil || state.Detections["N20|2026-11-25|1704|-0.00021|110.82748"].FRP != 3.27 {
		t.Fatalf("patch did not reach the detection: %v", err)
	}
}
