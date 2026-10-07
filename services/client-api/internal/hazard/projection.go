package hazard

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

var (
	ErrForbidden  = errors.New("field access forbidden")
	ErrBadRequest = errors.New("invalid field request")
	ErrResponse   = errors.New("invalid canonical response")
)

var summaryFields = []string{"hazard_id", "source", "hazard_type", "severity", "area_name", "occurred_at", "ingested_at"}
var canonicalFields = []string{"hazard_id", "source", "source_ref_id", "hazard_type", "severity", "area_name",
	"latitude", "longitude", "occurred_at", "ingested_at", "attributes"}

// Project selects permitted canonical fields; scope must come from validated Auth claims.
// Source freshness is projected separately by the HTTP handler.
func Project(rows []json.RawMessage, scope string, query url.Values) ([]json.RawMessage, error) {
	if scope != "media" && scope != "field-team" && scope != "internal-ops" {
		return nil, ErrForbidden
	}
	if len(query["include_raw"]) > 1 || len(query["fields"]) > 1 {
		return nil, ErrBadRequest
	}
	if query.Has("include_raw") {
		value := query.Get("include_raw")
		if value != "true" && value != "false" {
			return nil, ErrBadRequest
		}
		if scope == "media" && value == "true" {
			return nil, ErrForbidden
		}
	}
	allowed := canonicalFields
	if scope == "media" {
		allowed = summaryFields
	}
	selected := allowed
	if query.Has("fields") {
		selected = nil
		for _, field := range strings.Split(query.Get("fields"), ",") {
			field = strings.TrimSpace(field)
			if !contains(canonicalFields, field) {
				return nil, ErrBadRequest
			}
			if !contains(allowed, field) {
				return nil, ErrForbidden
			}
			selected = append(selected, field)
		}
	}
	result := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		var input map[string]json.RawMessage
		if err := json.Unmarshal(row, &input); err != nil || input == nil {
			return nil, ErrResponse
		}
		output := make(map[string]json.RawMessage, len(selected))
		for _, field := range selected {
			value, exists := input[field]
			if !exists {
				return nil, ErrResponse
			}
			output[field] = value
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			return nil, ErrResponse
		}
		result = append(result, encoded)
	}
	return result, nil
}

func contains(fields []string, field string) bool {
	for _, candidate := range fields {
		if candidate == field {
			return true
		}
	}
	return false
}
