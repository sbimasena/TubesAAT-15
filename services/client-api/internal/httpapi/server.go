package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
)

type hazardLister interface {
	List(context.Context, url.Values, string) ([]byte, error)
}

type correlationKey struct{}

func NewHandler(client hazardLister, enableProvisionalHazards bool, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "client-api"})
	})
	// NOT FINAL: this opt-in integration route bypasses authentication and field projection.
	if enableProvisionalHazards {
		mux.HandleFunc("GET /internal/provisional/hazards", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Provisional-Endpoint", "true")
			w.Header().Set("Cache-Control", "no-store")
			id, _ := r.Context().Value(correlationKey{}).(string)
			payload, err := client.List(r.Context(), r.URL.Query(), id)
			if err != nil {
				// NOT FINAL: public error semantics await agreement with Member A and the team.
				status := http.StatusBadGateway
				var requestErr *aggregator.RequestError
				if errors.As(err, &requestErr) && requestErr.Kind == aggregator.ErrorTimeout {
					status = http.StatusGatewayTimeout
				}
				http.Error(w, "Aggregator request failed", status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(payload)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-ID")
		if id == "" || len(id) > 128 {
			id = newCorrelationID()
		}
		w.Header().Set("X-Correlation-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), correlationKey{}, id))
		wrapped := &statusWriter{ResponseWriter: w}
		started := time.Now()
		mux.ServeHTTP(wrapped, r)
		if logger != nil {
			status := wrapped.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("request complete", "service", "client-api", "correlation_id", id,
				"operation", r.URL.Path, "latency_ms", float64(time.Since(started).Microseconds())/1000,
				"status", status)
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
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
