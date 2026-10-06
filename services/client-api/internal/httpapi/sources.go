package httpapi

import (
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
)

// sourceResponse exposes only approved ingestion metadata to every client scope.
// Absent timestamps are explicit nulls; internal diagnostics are never projected.
type sourceResponse struct {
	Available         bool       `json:"available"`
	LastIngested      *time.Time `json:"last_ingested_at"`
	Stale             bool       `json:"stale"`
	StaleSince        *time.Time `json:"stale_since"`
	StaleAfterSeconds float64    `json:"stale_after_seconds"`
}

func projectSources(sources map[string]aggregator.SourceStatus) map[string]sourceResponse {
	result := make(map[string]sourceResponse, 2)
	for _, name := range []string{"BMKG", "PVMBG"} {
		status := sources[name]
		result[name] = sourceResponse{Available: status.Available, LastIngested: status.LastIngested,
			Stale: status.Stale, StaleSince: status.StaleSince, StaleAfterSeconds: status.StaleAfterSeconds}
	}
	return result
}
