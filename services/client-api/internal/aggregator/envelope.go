package aggregator

import (
	"encoding/json"
	"fmt"
	"time"
)

// SourceStatus describes Aggregator ingestion freshness, not the age of a hazard.
// Missing ingestion metadata does not prove that PostgreSQL has no stored data.
// Internal diagnostics are parsed here and omitted by the HTTP response projection.
type SourceStatus struct {
	Available         bool       `json:"available"`
	LastSuccess       *time.Time `json:"last_success_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	LastIngested      *time.Time `json:"last_ingested_at,omitempty"`
	IngestionError    string     `json:"ingestion_error,omitempty"`
	Stale             bool       `json:"stale"`
	StaleSince        *time.Time `json:"stale_since,omitempty"`
	StaleAfterSeconds float64    `json:"stale_after_seconds"`
}

type Envelope struct {
	Data    []json.RawMessage
	Count   int
	Sources map[string]SourceStatus
}

// DecodeEnvelope validates data/count and both source statuses in the M1 read contract.
// Freshness is preserved for the HTTP projection, including empty query results.
func DecodeEnvelope(data []byte) (Envelope, error) {
	var wire struct {
		Data    []json.RawMessage          `json:"data"`
		Count   *int                       `json:"count"`
		Sources map[string]json.RawMessage `json:"sources"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || wire.Data == nil || wire.Count == nil ||
		*wire.Count != len(wire.Data) || wire.Sources == nil {
		return Envelope{}, fmt.Errorf("invalid Aggregator envelope")
	}
	result := Envelope{Data: wire.Data, Count: *wire.Count, Sources: make(map[string]SourceStatus, 2)}
	for _, name := range []string{"BMKG", "PVMBG"} {
		var required struct {
			Available         *bool    `json:"available"`
			Stale             *bool    `json:"stale"`
			StaleAfterSeconds *float64 `json:"stale_after_seconds"`
		}
		payload := wire.Sources[name]
		if err := json.Unmarshal(payload, &required); err != nil || required.Available == nil ||
			required.Stale == nil || required.StaleAfterSeconds == nil || *required.StaleAfterSeconds <= 0 {
			return Envelope{}, fmt.Errorf("invalid Aggregator source status")
		}
		var status SourceStatus
		if err := json.Unmarshal(payload, &status); err != nil {
			return Envelope{}, fmt.Errorf("invalid Aggregator source status")
		}
		result.Sources[name] = status
	}
	return result, nil
}
