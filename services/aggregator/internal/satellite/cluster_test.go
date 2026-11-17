package satellite

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

func detection(satellite, clock, lon string) Detection {
	return Detection{Satellite: satellite, AcqDate: "2026-11-12", AcqTime: clock, Latitude: "-2.51230", Longitude: lon, Confidence: "n", FRP: 15}
}

func TestClusterTransitiveDedupAndReplay(t *testing.T) {
	a := detection("N20", "0", "113.80000")
	b := detection("N21", "600", "113.80800")
	b.Confidence = "h"
	c := detection("N20", "1200", "113.81600")
	single := detection("N20", "1200", "114.00000")
	low := detection("N20", "1200", "114.10000")
	low.Confidence = "low"
	input := []Detection{c, a, b, a, single, low}
	fires, singles, err := Cluster(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || singles != 1 {
		t.Fatalf("fires=%d singles=%d", len(fires), singles)
	}
	fire := fires[0]
	if len(fire.DetectionKeys) != 3 || fire.TotalFRPMW != 45 || fire.Severity != "SIAGA" || fire.MaxConfidence != "h" || len(fire.Satellites) != 2 {
		t.Fatalf("%+v", fire)
	}
	if fire.DetectionKeys[0] != a.Key() || a.Key() != "N20|2026-11-12|0|-2.51230|113.80000" {
		t.Fatal("raw tokens changed")
	}
	replay, _, err := Cluster([]Detection{low, single, b, a, c})
	if err != nil || !reflect.DeepEqual(fires, replay) {
		t.Fatalf("replay changed result: %v", err)
	}
}

func TestTimeAndDistanceLimits(t *testing.T) {
	a := detection("N20", "0", "113.80000")
	for _, test := range []struct {
		time, lon string
		want      int
	}{{"600", "113.80800", 1}, {"601", "113.80800", 0}, {"600", "113.82000", 0}} {
		fires, _, err := Cluster([]Detection{a, detection("N21", test.time, test.lon)})
		if err != nil || len(fires) != test.want {
			t.Fatalf("%+v: fires=%d err=%v", test, len(fires), err)
		}
	}
}

func TestClusterRejectsInvalidAndConflictingDetections(t *testing.T) {
	a := detection("N20", "243", "113.80000")
	for _, change := range []func(*Detection){func(d *Detection) { d.Latitude = "NaN" }, func(d *Detection) { d.Longitude = "181" }, func(d *Detection) { d.FRP = math.Inf(1) }, func(d *Detection) { d.AcqTime = "2400" }, func(d *Detection) { d.Confidence = "unknown" }, func(d *Detection) { d.AcqDate = "2026-02-30" }} {
		bad := a
		change(&bad)
		if _, _, err := Cluster([]Detection{bad}); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	changed := a
	changed.FRP++
	if _, _, err := Cluster([]Detection{a, changed}); err == nil {
		t.Fatal("conflicting snapshot accepted")
	}
}

func TestSeverityThresholds(t *testing.T) {
	for _, tc := range []struct {
		n        int
		frp      float64
		severity string
	}{{2, 0, "WASPADA"}, {6, 0, "SIAGA"}, {20, 0, "AWAS"}, {2, 15, "SIAGA"}, {2, 50, "AWAS"}} {
		input := make([]Detection, tc.n)
		for i := range input {
			input[i] = detection("N20", fmt.Sprint(i), "113.80000")
			input[i].FRP = tc.frp
		}
		fires, _, err := Cluster(input)
		if err != nil || len(fires) != 1 || fires[0].Severity != tc.severity {
			t.Fatalf("%+v: %v %v", tc, fires, err)
		}
	}
}
