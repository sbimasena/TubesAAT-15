package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type HazardEvent struct {
	HazardID    string         `json:"hazard_id"`
	Source      string         `json:"source"`
	SourceRefID string         `json:"source_ref_id"`
	HazardType  string         `json:"hazard_type"`
	Severity    string         `json:"severity"`
	AreaName    string         `json:"area_name"`
	Latitude    float64        `json:"latitude"`
	Longitude   float64        `json:"longitude"`
	OccurredAt  time.Time      `json:"occurred_at"`
	IngestedAt  time.Time      `json:"ingested_at"`
	Attributes  map[string]any `json:"attributes"`
}

type SeismicEvent struct {
	EventID          string    `json:"event_id"`
	RegionName       string    `json:"region_name"`
	EpicenterLat     float64   `json:"epicenter_lat"`
	EpicenterLon     float64   `json:"epicenter_lon"`
	Magnitude        float64   `json:"magnitude"`
	DepthKM          float64   `json:"depth_km"`
	PotentialTsunami bool      `json:"potential_tsunami"`
	OccurredAt       time.Time `json:"occurred_at"`
}

type TsunamiWarning struct {
	WarningID   string    `json:"warning_id"`
	EventID     string    `json:"event_id"`
	ThreatLevel string    `json:"threat_level"`
	AreaName    string    `json:"area_name"`
	IssuedAt    time.Time `json:"issued_at"`
}

type VolcanicReport struct {
	ReportID         string                     `json:"report_id"`
	VolcanoID        string                     `json:"volcano_id"`
	AlertLevel       string                     `json:"alert_level"`
	ReportedAt       time.Time                  `json:"reported_at"`
	EruptionCount24H int                        `json:"eruption_count_24h"`
	AshColumnHeightM float64                    `json:"ash_column_height_m"`
	ConfidenceLevel  *float64                   `json:"confidence_level,omitempty"`
	Extra            map[string]json.RawMessage `json:"-"`
}

func (r *VolcanicReport) UnmarshalJSON(payload []byte) error {
	type known VolcanicReport
	var decoded known
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return err
	}
	for _, key := range []string{
		"report_id", "volcano_id", "alert_level", "reported_at",
		"eruption_count_24h", "ash_column_height_m", "confidence_level",
	} {
		delete(fields, key)
	}
	*r = VolcanicReport(decoded)
	r.Extra = fields
	return nil
}

func MapSeismic(event SeismicEvent, warning *TsunamiWarning, ingestedAt time.Time) HazardEvent {
	severity := seismicSeverity(event.Magnitude)
	attributes := map[string]any{
		"magnitude":         event.Magnitude,
		"depth_km":          event.DepthKM,
		"potential_tsunami": event.PotentialTsunami,
	}
	if warning != nil {
		if mapped := normalizeSeverity(warning.ThreatLevel); mapped != "" {
			severity = mapped
		}
		attributes["tsunami_warning"] = map[string]any{
			"warning_id":   warning.WarningID,
			"threat_level": warning.ThreatLevel,
			"area_name":    warning.AreaName,
			"issued_at":    warning.IssuedAt,
		}
	}
	return HazardEvent{
		HazardID:    hazardID("BMKG", event.EventID),
		Source:      "BMKG",
		SourceRefID: event.EventID,
		HazardType:  "SEISMIC",
		Severity:    severity,
		AreaName:    event.RegionName,
		Latitude:    event.EpicenterLat,
		Longitude:   event.EpicenterLon,
		OccurredAt:  event.OccurredAt.UTC(),
		IngestedAt:  ingestedAt.UTC(),
		Attributes:  attributes,
	}
}

func MapVolcanic(report VolcanicReport, ingestedAt time.Time) (HazardEvent, error) {
	volcano, ok := volcanoReference[report.VolcanoID]
	if !ok {
		return HazardEvent{}, fmt.Errorf("unknown PVMBG volcano_id %q", report.VolcanoID)
	}
	attributes := map[string]any{
		"eruption_count_24h":  report.EruptionCount24H,
		"ash_column_height_m": report.AshColumnHeightM,
	}
	if report.ConfidenceLevel != nil {
		attributes["confidence_level"] = *report.ConfidenceLevel
	}
	for key, raw := range report.Extra {
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			attributes[key] = value
		} else {
			attributes[key] = string(raw)
		}
	}
	return HazardEvent{
		HazardID:    hazardID("PVMBG", report.ReportID),
		Source:      "PVMBG",
		SourceRefID: report.ReportID,
		HazardType:  "VOLCANIC",
		Severity:    severityOr(report.AlertLevel, "NORMAL"),
		AreaName:    volcano.name,
		Latitude:    volcano.latitude,
		Longitude:   volcano.longitude,
		OccurredAt:  report.ReportedAt.UTC(),
		IngestedAt:  ingestedAt.UTC(),
		Attributes:  attributes,
	}, nil
}

func hazardID(source, sourceRefID string) string {
	digest := sha256.Sum256([]byte(source + ":" + sourceRefID))
	return "haz_" + hex.EncodeToString(digest[:16])
}

func seismicSeverity(magnitude float64) string {
	switch {
	case magnitude >= 6.5:
		return "SIAGA"
	case magnitude >= 5.0:
		return "WASPADA"
	default:
		return "NORMAL"
	}
}

func normalizeSeverity(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch value {
	case "NORMAL", "WASPADA", "SIAGA", "AWAS":
		return value
	case "ADVISORY":
		return "WASPADA"
	case "WATCH":
		return "SIAGA"
	case "WARNING":
		return "AWAS"
	default:
		return ""
	}
}

func severityOr(value, fallback string) string {
	if severity := normalizeSeverity(value); severity != "" {
		return severity
	}
	return fallback
}

type volcano struct {
	name      string
	latitude  float64
	longitude float64
}

var volcanoReference = map[string]volcano{
	"V-001": {"Merapi", -7.5407, 110.4457},
	"V-002": {"Semeru", -8.1080, 112.9220},
	"V-003": {"Anak Krakatau", -6.1020, 105.4230},
	"V-004": {"Sinabung", 3.1700, 98.3920},
	"V-005": {"Agung", -8.3420, 115.5080},
	"V-006": {"Marapi", -0.3810, 100.4730},
}
