package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestStoredReadsRemainSuccessfulWithExplicitSourceMetadata(t *testing.T) {
	for _, scope := range []string{"media", "field-team", "internal-ops"} {
		for _, scenario := range []string{"fresh", "cached during outage", "empty filter", "metadata reset", "no observed ingestion"} {
			t.Run(scope+"/"+scenario, func(t *testing.T) {
				payload := []byte(testHazardResponse)
				var wire map[string]any
				if err := json.Unmarshal(payload, &wire); err != nil {
					t.Fatal(err)
				}
				source := wire["sources"].(map[string]any)["BMKG"].(map[string]any)
				if scenario == "cached during outage" || scenario == "no observed ingestion" {
					source["available"], source["stale"] = false, true
					source["stale_since"] = "2026-10-06T00:00:02Z"
				}
				if scenario == "metadata reset" || scenario == "no observed ingestion" {
					delete(source, "last_ingested_at")
					source["stale"] = true
				}
				if scenario == "empty filter" || scenario == "no observed ingestion" {
					wire["data"], wire["count"] = []any{}, float64(0)
				}
				payload, _ = json.Marshal(wire)
				lister := listerFunc(func(context.Context, url.Values, string) ([]byte, error) { return payload, nil })
				handler := NewHandler(lister, &testValidator{scope: scope}, false, 64, nil)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, hazardRequest("/hazards?source=BMKG"))
				var public map[string]json.RawMessage
				var rows []map[string]any
				var statuses map[string]map[string]any
				var count int
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &public) != nil || len(public) != 3 ||
					json.Unmarshal(public["data"], &rows) != nil || rows == nil || json.Unmarshal(public["count"], &count) != nil ||
					json.Unmarshal(public["sources"], &statuses) != nil || len(statuses) != 2 || count != len(rows) {
					t.Fatal("successful stored query became an error or lost source metadata")
				}
				metadata := statuses["BMKG"]
				if len(metadata) != 5 || metadata["available"] != source["available"] || metadata["stale"] != source["stale"] ||
					metadata["last_ingested_at"] != source["last_ingested_at"] || metadata["stale_since"] != source["stale_since"] {
					t.Fatal("approved freshness metadata changed or was inferred from query results")
				}
				if scenario == "metadata reset" && len(rows) != 1 {
					t.Fatal("stored rows were dropped because ingestion metadata reset")
				}
				for _, row := range rows {
					want := 11
					if scope == "media" {
						want = 7
					}
					if len(row) != want {
						t.Fatal("freshness metadata bypassed the hazard scope projection")
					}
				}
			})
		}
	}
}
