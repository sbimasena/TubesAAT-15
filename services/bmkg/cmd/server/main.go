package main

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultPort       = "8081"
	defaultAPIKey     = "dev-bmkg-key"
	maxCatalogRecords = 1000
)

type seismicEvent struct {
	EventID          string    `json:"event_id"`
	RegionName       string    `json:"region_name"`
	EpicenterLat     float64   `json:"epicenter_lat"`
	EpicenterLon     float64   `json:"epicenter_lon"`
	Magnitude        float64   `json:"magnitude"`
	DepthKM          float64   `json:"depth_km"`
	PotentialTsunami bool      `json:"potential_tsunami"`
	OccurredAt       time.Time `json:"occurred_at"`
}

type tsunamiWarning struct {
	WarningID   string    `json:"warning_id"`
	EventID     string    `json:"event_id"`
	ThreatLevel string    `json:"threat_level"`
	AreaName    string    `json:"area_name"`
	IssuedAt    time.Time `json:"issued_at"`
}

type catalog struct {
	mu       sync.RWMutex
	events   []seismicEvent
	warnings []tsunamiWarning
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

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	apiKey := envOr("BMKG_API_KEY", defaultAPIKey)
	servicePort := envOr("BMKG_PORT", defaultPort)
	data := newCatalog(time.Now().UTC())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go data.generatePeriodically(ctx, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "bmkg"})
	})
	mux.Handle("GET /seismic-events", requireBMKGKey(apiKey, http.HandlerFunc(data.seismicEvents)))
	mux.Handle("GET /tsunami-warnings", requireBMKGKey(apiKey, http.HandlerFunc(data.tsunamiWarnings)))

	server := &http.Server{
		Addr:              ":" + servicePort,
		Handler:           withRequestLogging(logger, "bmkg", mux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed", "error", err)
		}
	}()

	logger.Info("service starting", "service", "bmkg", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func newCatalog(now time.Time) *catalog {
	regions := []struct {
		name string
		lat  float64
		lon  float64
	}{
		{"Selat Sunda", -6.2, 105.4},
		{"Pesisir Selatan Jawa", -8.1, 110.2},
		{"Laut Banda", -6.4, 129.8},
		{"Kepulauan Mentawai", -2.1, 99.8},
		{"Laut Maluku", 1.1, 126.4},
		{"Pesisir Barat Sumatra", -3.7, 100.4},
		{"Laut Flores", -7.6, 121.0},
		{"Teluk Tomini", 0.4, 121.5},
	}
	result := &catalog{events: make([]seismicEvent, 0, 32), warnings: make([]tsunamiWarning, 0, 8)}
	for i := 0; i < 20; i++ {
		region := regions[i%len(regions)]
		occurredAt := now.Add(-time.Duration(20-i) * 6 * time.Hour)
		magnitude := 3.2 + float64((i*7)%43)/10
		eventID := fmt.Sprintf("BMKG-%s-%03d", now.Format("20060102"), i+1)
		event := seismicEvent{
			EventID:          eventID,
			RegionName:       region.name,
			EpicenterLat:     region.lat,
			EpicenterLon:     region.lon,
			Magnitude:        magnitude,
			DepthKM:          float64(8 + (i*13)%180),
			PotentialTsunami: magnitude >= 5.8,
			OccurredAt:       occurredAt,
		}
		result.events = append(result.events, event)
		if event.PotentialTsunami {
			result.warnings = append(result.warnings, tsunamiWarning{
				WarningID:   "TSU-" + eventID,
				EventID:     eventID,
				ThreatLevel: []string{"WASPADA", "SIAGA", "AWAS"}[i%3],
				AreaName:    region.name,
				IssuedAt:    occurredAt.Add(2 * time.Minute),
			})
		}
	}
	return result
}

func (c *catalog) generatePeriodically(ctx context.Context, logger *slog.Logger) {
	interval := durationFromEnv("BMKG_GENERATE_INTERVAL_SECONDS", 15*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			event, warning := makeLiveEvent(now.UTC())
			c.mu.Lock()
			c.events = append(c.events, event)
			if warning != nil {
				c.warnings = append(c.warnings, *warning)
			}
			if len(c.events) > maxCatalogRecords {
				c.events = append([]seismicEvent(nil), c.events[len(c.events)-maxCatalogRecords:]...)
			}
			if len(c.warnings) > maxCatalogRecords {
				c.warnings = append([]tsunamiWarning(nil), c.warnings[len(c.warnings)-maxCatalogRecords:]...)
			}
			c.mu.Unlock()
			logger.Info("simulated event generated", "service", "bmkg", "source_ref_id", event.EventID, "potential_tsunami", event.PotentialTsunami)
		}
	}
}

func makeLiveEvent(now time.Time) (seismicEvent, *tsunamiWarning) {
	regions := []struct {
		name string
		lat  float64
		lon  float64
	}{
		{"Selat Sunda", -6.2, 105.4},
		{"Pesisir Selatan Jawa", -8.1, 110.2},
		{"Laut Banda", -6.4, 129.8},
		{"Kepulauan Mentawai", -2.1, 99.8},
		{"Laut Maluku", 1.1, 126.4},
	}
	region := regions[rand.IntN(len(regions))]
	eventID := fmt.Sprintf("BMKG-%d", now.UnixNano())
	magnitude := 3.0 + float64(rand.IntN(45))/10
	event := seismicEvent{
		EventID:          eventID,
		RegionName:       region.name,
		EpicenterLat:     region.lat + (rand.Float64()-0.5)*0.6,
		EpicenterLon:     region.lon + (rand.Float64()-0.5)*0.6,
		Magnitude:        magnitude,
		DepthKM:          float64(5 + rand.IntN(195)),
		PotentialTsunami: magnitude >= 5.8,
		OccurredAt:       now,
	}
	if !event.PotentialTsunami || rand.IntN(3) != 0 {
		return event, nil
	}
	warning := &tsunamiWarning{
		WarningID:   "TSU-" + eventID,
		EventID:     eventID,
		ThreatLevel: []string{"WASPADA", "SIAGA", "AWAS"}[rand.IntN(3)],
		AreaName:    region.name,
		IssuedAt:    now.Add(time.Minute),
	}
	return event, warning
}

func (c *catalog) seismicEvents(w http.ResponseWriter, r *http.Request) {
	if !waitForSimulatedDelay(w, r, time.Duration(50+rand.IntN(101))*time.Millisecond) {
		return
	}
	since, ok := parseSince(w, r)
	if !ok {
		return
	}
	c.mu.RLock()
	result := make([]seismicEvent, 0, len(c.events))
	for _, event := range c.events {
		if since.IsZero() || !event.OccurredAt.Before(since) {
			result = append(result, event)
		}
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].OccurredAt.Before(result[j].OccurredAt) })
	writeJSON(w, http.StatusOK, result)
}

func (c *catalog) tsunamiWarnings(w http.ResponseWriter, r *http.Request) {
	if !waitForSimulatedDelay(w, r, time.Duration(50+rand.IntN(101))*time.Millisecond) {
		return
	}
	since, ok := parseSince(w, r)
	if !ok {
		return
	}
	c.mu.RLock()
	result := make([]tsunamiWarning, 0, len(c.warnings))
	for _, warning := range c.warnings {
		if since.IsZero() || !warning.IssuedAt.Before(since) {
			result = append(result, warning)
		}
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].IssuedAt.Before(result[j].IssuedAt) })
	writeJSON(w, http.StatusOK, result)
}

func requireBMKGKey(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-BMKG-Key")
		if len(provided) != len(key) || subtle.ConstantTimeCompare([]byte(provided), []byte(key)) != 1 {
			http.Error(w, "invalid BMKG credential", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseSince(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	value := r.URL.Query().Get("since")
	if value == "" {
		return time.Time{}, true
	}
	since, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		http.Error(w, "since must be an RFC3339 timestamp", http.StatusBadRequest)
		return time.Time{}, false
	}
	return since, true
}

func waitForSimulatedDelay(w http.ResponseWriter, r *http.Request, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

func withRequestLogging(logger *slog.Logger, service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlationID := r.Header.Get("X-Correlation-ID")
		if correlationID == "" || len(correlationID) > 128 {
			correlationID = newCorrelationID()
		}
		w.Header().Set("X-Correlation-ID", correlationID)
		wrapped := &statusWriter{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(wrapped, r)
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		logger.Info("request complete", "service", service, "correlation_id", correlationID, "operation", r.URL.Path, "latency_ms", float64(time.Since(started).Microseconds())/1000, "status", status)
	})
}

func newCorrelationID() string {
	bytes := make([]byte, 16)
	if _, err := cryptorand.Read(bytes); err != nil {
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

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	seconds, err := time.ParseDuration(value + "s")
	if err != nil || seconds <= 0 {
		return fallback
	}
	return seconds
}
