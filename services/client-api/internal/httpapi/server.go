package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/hazard"
)

type hazardLister interface {
	List(context.Context, url.Values, string) ([]byte, error)
}

type correlationKey struct{}

func NewHandler(client hazardLister, validator accessValidator, enableProvisionalHazards bool, maxConcurrent int, logger *slog.Logger) http.Handler {
	if maxConcurrent < 1 {
		panic("hazard concurrency limit must be positive")
	}
	permits := make(chan struct{}, maxConcurrent)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "client-api"})
	})
	serveHazards := limitRequests(permits, authenticate(validator, func(w http.ResponseWriter, r *http.Request) {
		info := r.Context().Value(identityKey{}).(*requestIdentity)
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query", "invalid hazard query")
			return
		}
		// Validate requested permissions before making any Aggregator call.
		if _, err := hazard.Project(nil, info.Scope, query); err != nil {
			projectionError(w, err)
			return
		}
		id, _ := r.Context().Value(correlationKey{}).(string)
		payload, err := client.List(r.Context(), query, id)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			aggregatorError(w, err)
			return
		}
		envelope, err := aggregator.DecodeEnvelope(payload)
		if err != nil {
			writeError(w, http.StatusBadGateway, "invalid_aggregator_response", "invalid Aggregator response")
			return
		}
		rows, err := hazard.Project(envelope.Data, info.Scope, query)
		if err != nil {
			projectionError(w, err)
			return
		}
		// Successful stored-data reads stay 200 even during upstream outage or for empty filters.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "count": envelope.Count,
			"sources": projectSources(envelope.Sources)})
	}))
	mux.HandleFunc("GET /hazards", serveHazards)
	// NOT FINAL: alias retirement awaits a checkpoint migration decision.
	// Opt-in requests use the same Auth, projection, and admission limit as /hazards.
	if enableProvisionalHazards {
		mux.HandleFunc("GET /internal/provisional/hazards", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Provisional-Endpoint", "true")
			serveHazards(w, r)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-ID")
		if id == "" || len(id) > 128 {
			id = newCorrelationID()
		}
		w.Header().Set("X-Correlation-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		r = r.WithContext(context.WithValue(r.Context(), correlationKey{}, id))
		info := &requestIdentity{}
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, info))
		wrapped := &statusWriter{ResponseWriter: w}
		started := time.Now()
		mux.ServeHTTP(wrapped, r)
		if logger != nil {
			status := wrapped.status
			canceled := r.Context().Err() != nil && status == 0
			if status == 0 && !canceled {
				status = http.StatusOK
			}
			attrs := []any{"service", "client-api", "correlation_id", id,
				"operation", r.URL.Path, "latency_ms", float64(time.Since(started).Microseconds()) / 1000,
				"status", status}
			if info.ClientID != "" {
				attrs = append(attrs, "client_id", info.ClientID, "scope", info.Scope)
			}
			if canceled {
				attrs = append(attrs, "error_kind", "canceled")
			} else if wrapped.errorCode != "" {
				attrs = append(attrs, "error_kind", wrapped.errorCode)
			}
			logger.Info("request complete", attrs...)
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status    int
	errorCode string
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func newCorrelationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
