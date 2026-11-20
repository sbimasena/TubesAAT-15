package pantai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
)

type Station struct {
	StationID string  `json:"station_id"`
	Name      string  `json:"name"`
	Province  string  `json:"province"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Observation struct {
	ObservationID    string    `json:"observation_id"`
	StationID        string    `json:"station_id"`
	ObservedAt       time.Time `json:"observed_at"`
	SeaLevelAnomalyM float64   `json:"sea_level_anomaly_m"`
	WaveHeightM      float64   `json:"wave_height_m"`
	Quality          string    `json:"quality"`
	Critical         bool      `json:"critical"`
}

type Record struct {
	Raw         []byte
	Observation Observation
	Invalid     error
}

type Batch struct {
	GeneratedAt time.Time
	NextCursor  *string
	Records     []Record
	Raw         []byte // complete envelope for validation and metadata quarantine
	Invalid     error
	Truncated   bool
}

// Violation supplies a stable, non-retryable DLQ reason without payload data.
type Violation struct{ Detail string }

func (e *Violation) Error() string  { return "contract_violation:" + e.Detail }
func (e *Violation) Unwrap() error  { return deadletter.ErrContractViolation }
func violation(detail string) error { return &Violation{Detail: detail} }

// object rejects ambiguous duplicate keys, null required fields and excessive
// nesting before typed decoding. Unknown additive fields remain accepted.
func object(raw []byte, required ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, violation("invalid_utf8")
	}
	depth, quoted, escaped := 0, false, false
	for _, ch := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
			continue
		}
		switch ch {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > 128 {
				return nil, violation("too_deep")
			}
		case '}', ']':
			depth--
		}
	}
	if !json.Valid(raw) {
		return nil, violation("malformed_json")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, _ := decoder.Token()
	if token != json.Delim('{') {
		return nil, violation("object_required")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, _ := decoder.Token()
		key := token.(string)
		if _, exists := fields[key]; exists {
			return nil, violation("duplicate_field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, violation("malformed_json")
		}
		fields[key] = value
	}
	for _, key := range required {
		if value, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, violation("required_" + key)
		}
	}
	return fields, nil
}

func decodeStations(raw []byte) ([]Station, error) {
	fields, err := object(raw, "items")
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(fields["items"], &items); err != nil {
		return nil, violation("stations_items_type")
	}
	stations := make([]Station, 0, len(items))
	seen := map[string]bool{}
	for _, raw := range items {
		fields, err := object(raw, "station_id", "name", "province", "latitude", "longitude")
		if err != nil {
			return nil, err
		}
		var station Station
		if err := decodeFields(fields, map[string]any{"station_id": &station.StationID, "name": &station.Name,
			"province": &station.Province, "latitude": &station.Latitude, "longitude": &station.Longitude}); err != nil {
			return nil, violation("station_field_type")
		}
		if strings.TrimSpace(station.StationID) == "" || strings.TrimSpace(station.Name) == "" || strings.TrimSpace(station.Province) == "" {
			return nil, violation("station_blank_identity")
		}
		if math.Abs(station.Latitude) > 90 || math.Abs(station.Longitude) > 180 {
			return nil, violation("station_coordinates")
		}
		if seen[station.StationID] {
			return nil, violation("duplicate_station")
		}
		seen[station.StationID] = true
		stations = append(stations, station)
	}
	return stations, nil
}

func decodeObservation(raw []byte) (Observation, error) {
	var observation Observation
	if len(raw) > 1<<20 {
		return observation, violation("observation_too_large")
	}
	fields, err := object(raw, "observation_id", "station_id", "observed_at", "sea_level_anomaly_m", "wave_height_m", "quality", "critical")
	if err != nil {
		return observation, err
	}
	if err := decodeFields(fields, map[string]any{"observation_id": &observation.ObservationID,
		"station_id": &observation.StationID, "observed_at": &observation.ObservedAt,
		"sea_level_anomaly_m": &observation.SeaLevelAnomalyM, "wave_height_m": &observation.WaveHeightM,
		"quality": &observation.Quality, "critical": &observation.Critical}); err != nil {
		return Observation{}, violation("observation_field_type")
	}
	if strings.TrimSpace(observation.ObservationID) == "" || strings.TrimSpace(observation.StationID) == "" || observation.ObservedAt.IsZero() || strings.TrimSpace(observation.Quality) == "" {
		return observation, violation("observation_blank_identity")
	}
	return observation, nil
}

func decodeFields(fields map[string]json.RawMessage, destinations map[string]any) error {
	// Decode exact contract names: encoding/json's case-insensitive struct
	// matching would let an additive "Critical" override canonical "critical".
	for name, destination := range destinations {
		if err := json.Unmarshal(fields[name], destination); err != nil {
			return err
		}
	}
	return nil
}

// DecodeBatch keeps invalid records separate so valid siblings can progress.
// Cursor loops, temporal freshness, quality policy and physical sensor ranges
// require worker state/policy; this decoder deliberately does not infer them.
func DecodeBatch(raw []byte) Batch {
	bad := func(err error) Batch { return Batch{Raw: raw, Invalid: err} }
	if len(raw) > responseLimit {
		return bad(violation("response_too_large"))
	}
	// Decode only the envelope first: invalid UTF-8 inside an individual record
	// must be preserved and rejected there, without rejecting valid siblings.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return bad(violation("malformed_envelope"))
	}
	var shell bytes.Buffer
	shell.WriteByte('{')
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
	seen := map[string]bool{}
	for decoder.More() {
		token, _ := decoder.Token()
		key := token.(string)
		if seen[key] {
			return bad(violation("duplicate_envelope_field"))
		}
		seen[key] = true
		var value json.RawMessage
		_ = decoder.Decode(&value)
		if key != "items" {
			encodedKey, _ := json.Marshal(key)
			fmt.Fprintf(&shell, "%s:%s,", encodedKey, value)
		}
	}
	shell.WriteString(`"items":[]}`)
	if _, err := object(shell.Bytes(), "generated_at", "items"); err != nil {
		return bad(err)
	}
	if _, ok := fields["next_cursor"]; !ok {
		return bad(violation("required_next_cursor"))
	}
	batch := Batch{Raw: raw}
	if err := json.Unmarshal(fields["generated_at"], &batch.GeneratedAt); err != nil || batch.GeneratedAt.IsZero() {
		return bad(violation("generated_at_type"))
	}
	if err := json.Unmarshal(fields["next_cursor"], &batch.NextCursor); err != nil {
		return bad(violation("cursor_type"))
	}
	if batch.NextCursor != nil && strings.TrimSpace(*batch.NextCursor) == "" {
		return bad(violation("empty_cursor"))
	}
	if !bytes.HasPrefix(bytes.TrimSpace(fields["items"]), []byte("[")) {
		return bad(violation("items_array_required"))
	}
	var items []json.RawMessage
	if err := json.Unmarshal(fields["items"], &items); err != nil {
		return bad(violation("items_array_required"))
	}
	for _, raw := range items {
		observation, err := decodeObservation(raw)
		batch.Records = append(batch.Records, Record{Raw: raw, Observation: observation, Invalid: err})
	}
	return batch
}
