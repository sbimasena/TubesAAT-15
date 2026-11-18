package satellite

import (
	_ "embed"
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

//go:embed provinces.json
var provinceData []byte
var provinceReference struct {
	Provinces []struct {
		Name                string
		Latitude, Longitude float64
	}
}

func init() {
	if err := json.Unmarshal(provinceData, &provinceReference); err != nil {
		panic(err)
	}
}

func nearestProvince(lat, lon float64) string {
	best := math.Inf(1)
	name := ""
	for _, province := range provinceReference.Provinces {
		d := distance(point{lat: lat, lon: lon}, point{lat: province.Latitude, lon: province.Longitude})
		if d < best {
			best, name = d, province.Name
		}
	}
	return name
}

func (f Fire) Hazard(ingested time.Time) domain.HazardEvent {
	return domain.HazardEvent{
		HazardID: domain.HazardID("SATELIT", f.SourceRefID), Source: "SATELIT", SourceRefID: f.SourceRefID, HazardType: "WILDFIRE", Severity: f.Severity,
		AreaName: nearestProvince(f.Latitude, f.Longitude), Latitude: f.Latitude, Longitude: f.Longitude, OccurredAt: f.OccurredAt, IngestedAt: ingested.UTC(),
		Attributes: map[string]any{"detection_count": len(f.DetectionKeys), "satellites": slices.Clone(f.Satellites), "max_confidence": f.MaxConfidence, "total_frp_mw": f.TotalFRPMW, "detection_keys": slices.Clone(f.DetectionKeys)},
	}
}
