package aggregator

import (
	"encoding/json"
	"testing"
)

func TestDecodeEnvelopeSeparatesReachabilityFromCommittedIngestion(t *testing.T) {
	const payload = `{"data":[],"count":0,"sources":{
		"BMKG":{"available":true,"last_success_at":"2026-10-06T00:00:03Z","last_ingested_at":"2026-10-06T00:00:01Z","ingestion_error":"mapping failed","stale":true,"stale_since":"2026-10-06T00:00:02Z","stale_after_seconds":15,"future_field":true},
		"PVMBG":{"available":false,"last_error":"outage","stale":true,"stale_since":"2026-10-06T00:00:00Z","stale_after_seconds":15}},"future_envelope_field":true}`
	envelope, err := DecodeEnvelope([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	bmkg, pvmbg := envelope.Sources["BMKG"], envelope.Sources["PVMBG"]
	if !bmkg.Available || !bmkg.Stale || bmkg.LastIngested == nil || bmkg.LastSuccess == nil ||
		!bmkg.LastSuccess.After(*bmkg.LastIngested) || bmkg.IngestionError != "mapping failed" {
		t.Fatal("successful fetch incorrectly replaces committed ingestion freshness")
	}
	if pvmbg.Available || !pvmbg.Stale || pvmbg.LastIngested != nil || pvmbg.StaleSince == nil || pvmbg.LastError != "outage" {
		t.Fatal("source without ingestion metadata was changed")
	}
}

func TestDecodeEnvelopeDoesNotInferOutageFromEmptyFilteredData(t *testing.T) {
	const payload = `{"data":[],"count":0,"sources":{
		"BMKG":{"available":true,"last_ingested_at":"2026-10-06T00:00:00Z","stale":false,"stale_after_seconds":15},
		"PVMBG":{"available":false,"last_ingested_at":"2026-10-05T00:00:00Z","stale":true,"stale_since":"2026-10-06T00:00:00Z","stale_after_seconds":15}}}`
	envelope, err := DecodeEnvelope([]byte(payload))
	if err != nil || envelope.Data == nil || envelope.Count != 0 || envelope.Sources["BMKG"].Stale ||
		envelope.Sources["PVMBG"].LastIngested == nil {
		t.Fatal("empty query result changed source freshness/history")
	}
}

func TestDecodeEnvelopeRejectsMissingOrMalformedContractFields(t *testing.T) {
	for _, mode := range []string{"missing count", "null count", "wrong count", "null data", "null sources",
		"missing source", "null source", "missing available", "null available", "wrong available",
		"missing stale", "invalid deadline", "missing deadline", "invalid timestamp"} {
		t.Run(mode, func(t *testing.T) {
			var wire map[string]any
			if err := json.Unmarshal([]byte(validResponse), &wire); err != nil {
				t.Fatal(err)
			}
			sources := wire["sources"].(map[string]any)
			bmkg := sources["BMKG"].(map[string]any)
			switch mode {
			case "missing count":
				delete(wire, "count")
			case "null count":
				wire["count"] = nil
			case "wrong count":
				wire["count"] = 1
			case "null data":
				wire["data"] = nil
			case "null sources":
				wire["sources"] = nil
			case "missing source":
				delete(sources, "PVMBG")
			case "null source":
				sources["PVMBG"] = nil
			case "missing available":
				delete(bmkg, "available")
			case "null available":
				bmkg["available"] = nil
			case "wrong available":
				bmkg["available"] = "yes"
			case "missing stale":
				delete(bmkg, "stale")
			case "invalid deadline":
				bmkg["stale_after_seconds"] = -1
			case "missing deadline":
				delete(bmkg, "stale_after_seconds")
			case "invalid timestamp":
				bmkg["last_ingested_at"] = "yesterday"
			}
			payload, _ := json.Marshal(wire)
			if _, err := DecodeEnvelope(payload); err == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}
