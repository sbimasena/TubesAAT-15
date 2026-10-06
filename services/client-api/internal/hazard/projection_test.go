package hazard

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"
)

const canonical = `{"hazard_id":"id","source":"PVMBG","source_ref_id":"ref","hazard_type":"VOLCANIC","severity":"SIAGA","area_name":"Merapi","latitude":-7.5,"longitude":110.4,"occurred_at":"2026-10-06T00:00:00Z","ingested_at":"2026-10-06T00:00:01Z","attributes":{"confidence_level":0.9,"extra":{"raw":"private"}},"future_raw":"private"}`

func TestProjectionEnforcesScopeAndPreservesPermittedAttributes(t *testing.T) {
	for _, scope := range []string{"media", "field-team", "internal-ops"} {
		rows, err := Project([]json.RawMessage{json.RawMessage(canonical)}, scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(rows[0], &result); err != nil {
			t.Fatal(err)
		}
		if scope == "media" {
			if len(result) != 7 || string(result["ingested_at"]) != `"2026-10-06T00:00:01Z"` {
				t.Fatal("Media response contains fields outside its allowlist")
			}
			for _, raw := range []string{"latitude", "longitude", "attributes", "source_ref_id", "future_raw"} {
				if _, exists := result[raw]; exists {
					t.Fatalf("Media received %s", raw)
				}
			}
		} else {
			if len(result) != len(canonicalFields) || string(result["attributes"]) != `{"confidence_level":0.9,"extra":{"raw":"private"}}` {
				t.Fatal("permitted canonical fields or additive attributes changed")
			}
		}
	}
}

func TestExplicitFieldRequests(t *testing.T) {
	for _, test := range []struct {
		query string
		want  error
	}{
		{"include_raw=true", ErrForbidden},
		{"fields=latitude,longitude,attributes", ErrForbidden},
		{"fields=source_ref_id", ErrForbidden},
		{"fields=unknown", ErrBadRequest},
		{"fields=", ErrBadRequest},
		{"include_raw=invalid", ErrBadRequest},
		{"include_raw=false&include_raw=true", ErrBadRequest},
		{"fields=source&fields=latitude", ErrBadRequest},
	} {
		query, _ := url.ParseQuery(test.query)
		if _, err := Project(nil, "media", query); !errors.Is(err, test.want) {
			t.Fatalf("%s: expected %v, got %v", test.query, test.want, err)
		}
	}
	query, _ := url.ParseQuery("fields=hazard_id,source")
	rows, err := Project([]json.RawMessage{json.RawMessage(canonical)}, "media", query)
	if err != nil || string(rows[0]) != `{"hazard_id":"id","source":"PVMBG"}` {
		t.Fatalf("summary field selection failed: %v", err)
	}
	if _, err := Project(nil, "unknown", nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("unknown scope accepted")
	}
	if _, err := Project([]json.RawMessage{json.RawMessage(`{"hazard_id":"id"}`)}, "media", nil); !errors.Is(err, ErrResponse) {
		t.Fatal("incomplete canonical response accepted")
	}
}
