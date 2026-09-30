package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultPort       = "8082"
	maxCatalogRecords = 1000
)

type volcanicReport struct {
	ReportID         string    `json:"report_id"`
	VolcanoID        string    `json:"volcano_id"`
	AlertLevel       string    `json:"alert_level"`
	ReportedAt       time.Time `json:"reported_at"`
	EruptionCount24H int       `json:"eruption_count_24h"`
	AshColumnHeightM float64   `json:"ash_column_height_m"`
	ConfidenceLevel  *float64  `json:"confidence_level,omitempty"`
}

type catalog struct {
	mu            sync.RWMutex
	reports       []volcanicReport
	schemaVersion int
	outage        atomic.Bool
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
	token := requiredEnv(logger, "PVMBG_TOKEN")
	servicePort := envOr("PVMBG_PORT", defaultPort)
	delay := configuredDelay()
	data := newCatalog(time.Now().UTC())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go data.generatePeriodically(ctx, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "service": "pvmbg", "simulated_outage": data.outage.Load(),
		})
	})
	mux.Handle("GET /volcanic-reports", requirePVMBGToken(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data.volcanicReports(w, r, delay)
	})))
	mux.Handle("POST /admin/schema-version", requirePVMBGToken(token, http.HandlerFunc(data.setSchemaVersion)))
	mux.Handle("POST /admin/outage", requirePVMBGToken(token, http.HandlerFunc(data.setOutage)))

	server := &http.Server{
		Addr:              ":" + servicePort,
		Handler:           withRequestLogging(logger, "pvmbg", mux),
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

	logger.Info("service starting", "service", "pvmbg", "addr", server.Addr, "simulated_delay_ms", delay.Milliseconds())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func newCatalog(now time.Time) *catalog {
	volcanoIDs := []string{"V-001", "V-002", "V-003", "V-004", "V-005", "V-006"}
	alerts := []string{"NORMAL", "WASPADA", "SIAGA", "AWAS"}
	result := &catalog{reports: make([]volcanicReport, 0, 32), schemaVersion: 1}
	for i := 0; i < 20; i++ {
		reportedAt := now.Add(-time.Duration(20-i) * 12 * time.Hour)
		result.reports = append(result.reports, volcanicReport{
			ReportID:         fmt.Sprintf("PVMBG-%s-%03d", now.Format("20060102"), i+1),
			VolcanoID:        volcanoIDs[i%len(volcanoIDs)],
			AlertLevel:       alerts[(i*3)%len(alerts)],
			ReportedAt:       reportedAt,
			EruptionCount24H: (i * 7) % 18,
			AshColumnHeightM: float64((i * 135) % 2400),
		})
	}
	return result
}

func (c *catalog) generatePeriodically(ctx context.Context, logger *slog.Logger) {
	interval := durationFromEnv("PVMBG_GENERATE_INTERVAL_SECONDS", 15*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.mu.Lock()
			report := makeLiveReport(now.UTC(), c.schemaVersion)
			c.reports = append(c.reports, report)
			if len(c.reports) > maxCatalogRecords {
				c.reports = append([]volcanicReport(nil), c.reports[len(c.reports)-maxCatalogRecords:]...)
			}
			c.mu.Unlock()
			logger.Info("simulated report generated", "service", "pvmbg", "source_ref_id", report.ReportID, "schema_version", c.schemaVersion)
		}
	}
}

func makeLiveReport(now time.Time, schemaVersion int) volcanicReport {
	volcanoIDs := []string{"V-001", "V-002", "V-003", "V-004", "V-005", "V-006"}
	alerts := []string{"NORMAL", "WASPADA", "SIAGA", "AWAS"}
	report := volcanicReport{
		ReportID:         fmt.Sprintf("PVMBG-%d", now.UnixNano()),
		VolcanoID:        volcanoIDs[rand.IntN(len(volcanoIDs))],
		AlertLevel:       alerts[rand.IntN(len(alerts))],
		ReportedAt:       now,
		EruptionCount24H: rand.IntN(25),
		AshColumnHeightM: float64(rand.IntN(3000)),
	}
	if schemaVersion >= 2 {
		confidence := 0.65 + rand.Float64()*0.34
		report.ConfidenceLevel = &confidence
	}
	return report
}

func (c *catalog) volcanicReports(w http.ResponseWriter, r *http.Request, delay time.Duration) {
	if c.outage.Load() {
		http.Error(w, "PVMBG is simulating an outage", http.StatusServiceUnavailable)
		return
	}
	if !waitForSimulatedDelay(w, r, delay) {
		return
	}
	since, ok := parseSince(w, r)
	if !ok {
		return
	}
	c.mu.RLock()
	result := make([]volcanicReport, 0, len(c.reports))
	for _, report := range c.reports {
		if since.IsZero() || !report.ReportedAt.Before(since) {
			result = append(result, report)
		}
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ReportedAt.Before(result[j].ReportedAt) })
	writeJSON(w, http.StatusOK, result)
}

func (c *catalog) setSchemaVersion(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "body must be JSON with version 1 or 2", http.StatusBadRequest)
		return
	}
	if request.Version != 1 && request.Version != 2 {
		http.Error(w, "supported schema versions are 1 and 2", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.schemaVersion = request.Version
	var addedReport *volcanicReport
	if request.Version == 2 {
		report := makeLiveReport(time.Now().UTC(), request.Version)
		c.reports = append(c.reports, report)
		if len(c.reports) > maxCatalogRecords {
			c.reports = append([]volcanicReport(nil), c.reports[len(c.reports)-maxCatalogRecords:]...)
		}
		addedReport = &report
	}
	c.mu.Unlock()
	response := map[string]any{"schema_version": request.Version, "confidence_level_enabled": request.Version >= 2}
	if addedReport != nil {
		response["report_id"] = addedReport.ReportID
	}
	writeJSON(w, http.StatusOK, response)
}

func (c *catalog) setOutage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "body must be JSON with an enabled boolean", http.StatusBadRequest)
		return
	}
	c.outage.Store(request.Enabled)
	writeJSON(w, http.StatusOK, map[string]bool{"simulated_outage": request.Enabled})
}

func requirePVMBGToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			http.Error(w, "invalid PVMBG credential", http.StatusUnauthorized)
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
	return fmt.Sprintf("%x", time.Now().UnixNano())
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

func requiredEnv(logger *slog.Logger, key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		logger.Error("required environment variable is missing", "name", key)
		os.Exit(1)
	}
	return value
}

func configuredDelay() time.Duration {
	value := strings.TrimSpace(os.Getenv("PVMBG_DELAY_MS"))
	if value == "" {
		return 750 * time.Millisecond
	}
	milliseconds, err := strconv.Atoi(value)
	if err != nil || milliseconds < 500 || milliseconds > 3000 {
		return 750 * time.Millisecond
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
