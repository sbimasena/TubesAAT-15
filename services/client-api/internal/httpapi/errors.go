package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/hazard"
)

// writeError uses fixed messages; dependency bodies and internal errors stay private.
func writeError(w http.ResponseWriter, status int, code, message string) {
	if writer, ok := w.(*statusWriter); ok {
		writer.errorCode = code
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":          map[string]string{"code": code, "message": message},
		"correlation_id": w.Header().Get("X-Correlation-ID"),
	})
}

func aggregatorError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusBadGateway, "aggregator_error", "Aggregator request failed"
	var requestErr *aggregator.RequestError
	if errors.As(err, &requestErr) {
		switch requestErr.Kind {
		case aggregator.ErrorTimeout:
			status, code, message = http.StatusGatewayTimeout, "aggregator_timeout", "Aggregator request timed out"
		case aggregator.ErrorInvalidResponse:
			code, message = "invalid_aggregator_response", "invalid Aggregator response"
		case aggregator.ErrorUpstreamStatus:
			switch requestErr.Status {
			case http.StatusBadRequest:
				status, code, message = http.StatusBadRequest, "invalid_query", "invalid hazard query"
			case http.StatusServiceUnavailable:
				status, code, message = http.StatusServiceUnavailable, "aggregator_unavailable", "Aggregator unavailable"
			}
		}
	}
	writeError(w, status, code, message)
}

func projectionError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusBadGateway, "invalid_aggregator_response", "invalid Aggregator response"
	if errors.Is(err, hazard.ErrForbidden) {
		status, code, message = http.StatusForbidden, "forbidden", "field access forbidden"
	}
	if errors.Is(err, hazard.ErrBadRequest) {
		status, code, message = http.StatusBadRequest, "invalid_fields", "invalid field request"
	}
	writeError(w, status, code, message)
}
