package pantai

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
)

const validObservation = `{"observation_id":"independent-id","station_id":"station-x","observed_at":"2026-11-12T00:00:00Z","sea_level_anomaly_m":-0.2,"wave_height_m":0,"quality":"ok","critical":false}`

func envelope(items string) []byte {
	return []byte(`{"generated_at":"2026-11-12T00:01:00Z","items":[` + items + `],"next_cursor":"opaque/+=="}`)
}

func TestObservationRequiredFieldsAndTypes(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(validObservation), &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		for _, kind := range []string{"missing", "null", "wrong-type"} {
			t.Run(key+"/"+kind, func(t *testing.T) {
				copy := make(map[string]json.RawMessage, len(fields))
				for name, value := range fields {
					copy[name] = value
				}
				switch kind {
				case "missing":
					delete(copy, key)
				case "null":
					copy[key] = json.RawMessage(`null`)
				case "wrong-type":
					copy[key] = json.RawMessage(`[]`)
				}
				raw, _ := json.Marshal(copy)
				batch := DecodeBatch(envelope(string(raw)))
				if batch.Invalid != nil || len(batch.Records) != 1 || !errors.Is(batch.Records[0].Invalid, deadletter.ErrContractViolation) {
					t.Fatalf("invalid record was not isolated: %+v", batch)
				}
			})
		}
	}
}

func TestObservationAdditiveFieldsDoNotOverrideCanonicalKeys(t *testing.T) {
	raw := strings.TrimSuffix(validObservation, "}") + `,"Critical":true,"vendor_extra":{"value":1}}`
	batch := DecodeBatch(envelope(raw))
	if batch.Invalid != nil || len(batch.Records) != 1 || batch.Records[0].Invalid != nil || batch.Records[0].Observation.Critical {
		t.Fatalf("canonical field overridden: %+v", batch)
	}
	if !bytes.Equal(batch.Records[0].Raw, []byte(raw)) || *batch.NextCursor != "opaque/+==" {
		t.Fatal("raw bytes or opaque cursor changed")
	}
}

func TestBadObservationPreservesRawAndValidSiblings(t *testing.T) {
	cases := map[string]string{
		"invalid_utf8":    strings.Replace(validObservation, "station-x", string([]byte{0xff, 0xfe}), 1),
		"duplicate_field": strings.TrimSuffix(validObservation, "}") + `,"critical":true}`,
		"oversize":        strings.TrimSuffix(validObservation, "}") + `,"extra":"` + strings.Repeat("x", 1<<20) + `"}`,
		"depth":           strings.TrimSuffix(validObservation, "}") + `,"extra":` + strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130) + "}",
		"scalar":          "false",
		"blank":           strings.Replace(validObservation, "independent-id", "  ", 1),
		"timestamp":       strings.Replace(validObservation, "2026-11-12T00:00:00Z", "not-a-date", 1),
		"overflow":        strings.Replace(validObservation, "-0.2", "1e999", 1),
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			batch := DecodeBatch(envelope(invalid + "," + validObservation))
			if batch.Invalid != nil || len(batch.Records) != 2 || batch.Records[0].Invalid == nil || batch.Records[1].Invalid != nil {
				t.Fatalf("sibling isolation failed: %+v", batch)
			}
			if !bytes.Equal(batch.Records[0].Raw, []byte(invalid)) {
				t.Fatal("invalid record wire bytes changed")
			}
		})
	}
}

func TestEnvelopeFailuresRetainWholeResponse(t *testing.T) {
	cases := []string{
		`null`, `[]`, `{`, `{"items":[],"next_cursor":null}`,
		`{"generated_at":"2026-11-12T00:00:00Z","items":null,"next_cursor":null}`,
		`{"generated_at":"2026-11-12T00:00:00Z","items":[],"next_cursor":4}`,
		`{"generated_at":"2026-11-12T00:00:00Z","items":[],"next_cursor":""}`,
		`{"generated_at":"2026-11-12T00:00:00Z","items":[],"next_cursor":null,"items":[]}`,
	}
	for _, raw := range cases {
		batch := DecodeBatch([]byte(raw))
		if batch.Invalid == nil || !bytes.Equal(batch.Raw, []byte(raw)) || len(batch.Records) != 0 {
			t.Fatalf("envelope incorrectly accepted: %.200s", raw)
		}
	}
	batch := DecodeBatch([]byte(`{"generated_at":"2026-11-12T00:00:00Z","items":[],"next_cursor":null}`))
	if batch.Invalid != nil || batch.NextCursor != nil {
		t.Fatalf("terminal empty page rejected: %+v", batch)
	}
}

func TestStationCatalogValidation(t *testing.T) {
	valid := `{"station_id":"s","name":"Coast","province":"Province","latitude":0,"longitude":180}`
	for _, raw := range []string{
		`{"items":null}`, `{"items":[null]}`, `{"items":[` + valid + `,` + valid + `]}`,
		`{"items":[` + strings.Replace(valid, `"latitude":0`, `"latitude":91`, 1) + `]}`,
		`{"items":[` + strings.Replace(valid, `"name":"Coast"`, `"name":""`, 1) + `]}`,
	} {
		if _, err := decodeStations([]byte(raw)); err == nil {
			t.Fatalf("invalid catalog accepted: %s", raw)
		}
	}
	if stations, err := decodeStations([]byte(`{"items":[` + valid + `]}`)); err != nil || len(stations) != 1 {
		t.Fatalf("valid catalog rejected: %v", err)
	}
}
