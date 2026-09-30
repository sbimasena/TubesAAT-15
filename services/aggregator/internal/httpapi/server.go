package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/ingest"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type Server struct {
	repository store.Repository
	manager    *ingest.Manager
	logger     *slog.Logger
}

func NewServer(repository store.Repository, manager *ingest.Manager, logger *slog.Logger) *Server {
	return &Server{repository: repository, manager: manager, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /hazards", s.listHazards)
	mux.HandleFunc("GET /internal/v1/hazards", s.listHazards)
	return s.withRequestLogging(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "service": "aggregator", "sources": s.manager.Status(),
	})
}

func (s *Server) listHazards(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	events, err := s.repository.List(r.Context(), filter)
	if err != nil {
		s.logger.Error("canonical query failed", "correlation_id", r.Header.Get("X-Correlation-ID"), "error", err)
		http.Error(w, "canonical query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": events, "count": len(events), "sources": s.manager.Status(),
	})
}

func parseFilter(r *http.Request) (store.Filter, error) {
	filter := store.Filter{Limit: 100}
	query := r.URL.Query()
	filter.Source = strings.ToUpper(strings.TrimSpace(query.Get("source")))
	if filter.Source != "" && filter.Source != "BMKG" && filter.Source != "PVMBG" {
		return store.Filter{}, fmt.Errorf("source must be BMKG or PVMBG")
	}
	filter.HazardType = strings.ToUpper(strings.TrimSpace(query.Get("hazard_type")))
	if filter.HazardType != "" && filter.HazardType != "SEISMIC" && filter.HazardType != "VOLCANIC" {
		return store.Filter{}, fmt.Errorf("hazard_type must be SEISMIC or VOLCANIC")
	}
	if value := query.Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return store.Filter{}, fmt.Errorf("since must be an RFC3339 timestamp")
		}
		filter.Since = parsed
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 1000 {
			return store.Filter{}, fmt.Errorf("limit must be between 1 and 1000")
		}
		filter.Limit = limit
	}
	return filter, nil
}

func (s *Server) withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlationID := r.Header.Get("X-Correlation-ID")
		if correlationID == "" || len(correlationID) > 128 {
			correlationID = newCorrelationID()
		}
		w.Header().Set("X-Correlation-ID", correlationID)
		r.Header.Set("X-Correlation-ID", correlationID)
		wrapped := &statusWriter{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(wrapped, r)
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		s.logger.Info("request complete", "service", "aggregator", "correlation_id", correlationID, "operation", r.URL.Path, "latency_ms", float64(time.Since(started).Microseconds())/1000, "status", status)
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

func (w *statusWriter) Write(payload []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(payload)
}

func newCorrelationID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("encode response", "error", err)
	}
}
