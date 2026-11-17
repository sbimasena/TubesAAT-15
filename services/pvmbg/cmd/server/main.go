package main

import (
	"bytes"
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
	// Variant is the schema version the report was generated with (P3:
	// incompatible and broken modes). It is never serialized directly; the
	// wire shape is derived from it in encodeReport.
	Variant int `json:"-"`
}

type catalog struct {
	mu            sync.RWMutex
	reports       []volcanicReport
	schemaVersion int
	outage        atomic.Bool
}

// P3 schema modes. Versions 1 and 2 are the compatible M1 contract
// (confidence_level is additive). Versions 3-9 are incompatible or broken
// modes used for the P3 demo: they are triggered by POST /admin/schema-version
// with either {"version": N} or {"mode": "<name>"}.
const (
	modeRename      = "rename_field"   // version 3: alert_level -> status
	modeChangeType  = "change_type"    // version 4: reported_at as unix seconds
	modeDropRequired = "drop_required" // version 5: alert_level omitted
	modeBrokenJSON  = "broken_json"    // version 6: response body truncated
	modeOversize    = "oversize_json"  // version 7: ~2 MiB padding per new report
	modeDeep        = "deep_json"      // version 8: deeply nested extra field
	modeInvalidUTF8 = "invalid_utf8"   // version 9: string with invalid UTF-8 bytes

	paddingSize  = 2 << 20 // 2 MiB: exceeds the documented per-message size limit
	nestingDepth = 20000   // exceeds the encoding/json decoder depth limit (10000)
)

var modeNames = map[int]string{
	3: modeRename,
	4: modeChangeType,
	5: modeDropRequired,
	6: modeBrokenJSON,
	7: modeOversize,
	8: modeDeep,
	9: modeInvalidUTF8,
}

var modeVersions = map[string]int{
	modeRename:      3,
	modeChangeType:  4,
	modeDropRequired: 5,
	modeBrokenJSON:  6,
	modeOversize:    7,
	modeDeep:        8,
	modeInvalidUTF8: 9,
}

func modeNameForVersion(version int) string {
	if name, ok := modeNames[version]; ok {
		return name
	}
	return "compatible"
}

func supportedModes() []string {
	names := make([]string, 0, len(modeVersions))
	for name := range modeVersions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		if err := checkHealth("http://127.0.0.1:" + envOr("PVMBG_PORT", defaultPort) + "/health"); err != nil {
			os.Exit(1)
		}
		return
	}
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
		data.mu.RLock()
		version := data.schemaVersion
		data.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "service": "pvmbg", "simulated_outage": data.outage.Load(),
			"schema_version": version, "schema_mode": modeNameForVersion(version),
			"simulated_delay_ms": delay.Milliseconds(),
		})
	})
	mux.Handle("GET /volcanic-reports", requirePVMBGToken(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data.volcanicReports(w, r, delay)
	})))
	mux.Handle("POST /admin/schema-version", http.HandlerFunc(data.setSchemaVersion))
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
			Variant:          1,
		})
	}
	return result
}

func (c *catalog) generatePeriodically(ctx context.Context, logger *slog.Logger) {
	interval := durationFromEnv("PVMBG_GENERATE_INTERVAL_SECONDS", 10*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.mu.Lock()
			report := makeLiveReport(now.UTC(), c.schemaVersion)
			report.Variant = c.schemaVersion
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
	selected := make([]volcanicReport, 0, len(c.reports))
	for _, report := range c.reports {
		if since.IsZero() || !report.ReportedAt.Before(since) {
			selected = append(selected, report)
		}
	}
	currentVersion := c.schemaVersion
	c.mu.RUnlock()
	sort.Slice(selected, func(i, j int) bool { return selected[i].ReportedAt.Before(selected[j].ReportedAt) })

	// P3: reports keep the wire shape of the variant they were generated with,
	// so records emitted before an incompatible switch remain parseable while
	// new records deliberately violate the contract.
	encoded := make([][]byte, 0, len(selected))
	for _, report := range selected {
		encoded = append(encoded, encodeReport(report))
	}
	payload := joinJSONArray(encoded)
	if currentVersion == modeVersions[modeBrokenJSON] {
		// broken_json corrupts the response itself: the body ends mid-document,
		// which the downstream client must handle as a malformed response
		// without crashing.
		payload = payload[:len(payload)/2]
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		slog.Error("write response", "error", err)
	}
}

// encodeReport serializes a report in the wire shape of its variant.
func encodeReport(report volcanicReport) []byte {
	if report.Variant <= 2 {
		data, err := json.Marshal(report)
		if err != nil {
			return []byte("{}")
		}
		return data
	}
	fields := reportToMap(report)
	switch report.Variant {
	case modeVersions[modeRename]:
		// Rename a required field: consumers looking for alert_level see it
		// missing even though the data is present under a different name.
		if level, ok := fields["alert_level"]; ok {
			fields["status"] = level
		}
		delete(fields, "alert_level")
	case modeVersions[modeChangeType]:
		// Change a type: reported_at becomes unix seconds instead of RFC3339.
		fields["reported_at"] = report.ReportedAt.Unix()
	case modeVersions[modeDropRequired]:
		// Remove a required field entirely.
		delete(fields, "alert_level")
	case modeVersions[modeOversize]:
		fields["padding"] = strings.Repeat("x", paddingSize)
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return []byte("{}")
	}
	if report.Variant == modeVersions[modeDeep] {
		// encoding/json enforces a ~10000 nesting limit on marshal AND
		// unmarshal, so the deeply nested payload is assembled as raw JSON
		// with a depth the downstream decoder must reject without crashing.
		return injectDeepNested(data)
	}
	if report.Variant == modeVersions[modeInvalidUTF8] {
		// encoding/json replaces invalid bytes during marshal, so the raw
		// bytes are injected after serialization via a sentinel value.
		sentinel := "__INVALID_UTF8__"
		fields["note"] = sentinel
		raw, err := json.Marshal(fields)
		if err != nil {
			return data
		}
		replacement := append([]byte{'"'}, 0xff, 0xfe, '"')
		return bytes.Replace(raw, []byte(`"`+sentinel+`"`), replacement, 1)
	}
	return data
}

func injectDeepNested(record []byte) []byte {
	nested := make([]byte, 0, 10*nestingDepth)
	for i := 0; i < nestingDepth; i++ {
		nested = append(nested, `{"child":`...)
	}
	nested = append(nested, `{"leaf":true}`...)
	for i := 0; i < nestingDepth; i++ {
		nested = append(nested, '}')
	}
	out := make([]byte, 0, len(record)+len(nested)+16)
	out = append(out, record[:len(record)-1]...)
	out = append(out, `,"extra":`...)
	out = append(out, nested...)
	return append(out, '}')
}

func reportToMap(report volcanicReport) map[string]any {
	fields := map[string]any{
		"report_id":           report.ReportID,
		"volcano_id":          report.VolcanoID,
		"alert_level":         report.AlertLevel,
		"reported_at":         report.ReportedAt,
		"eruption_count_24h":  report.EruptionCount24H,
		"ash_column_height_m": report.AshColumnHeightM,
	}
	if report.ConfidenceLevel != nil {
		fields["confidence_level"] = *report.ConfidenceLevel
	}
	return fields
}

// joinJSONArray concatenates individually encoded records. json.Marshal on the
// whole slice cannot be used because per-record shapes differ.
func joinJSONArray(records [][]byte) []byte {
	total := 2
	for _, record := range records {
		total += len(record) + 1
	}
	out := make([]byte, 0, total)
	out = append(out, '[')
	for i, record := range records {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, record...)
	}
	return append(out, ']')
}

func (c *catalog) setSchemaVersion(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version int    `json:"version"`
		Mode    string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "body must be JSON with version 1 or 2, or a supported mode", http.StatusBadRequest)
		return
	}
	version := request.Version
	if strings.TrimSpace(request.Mode) != "" {
		if request.Version != 0 {
			http.Error(w, "set either version or mode, not both", http.StatusBadRequest)
			return
		}
		mode, ok := modeVersions[strings.TrimSpace(request.Mode)]
		if !ok {
			http.Error(w, fmt.Sprintf("unsupported mode %q; supported modes: %s", request.Mode, strings.Join(supportedModes(), ", ")), http.StatusBadRequest)
			return
		}
		version = mode
	}
	if version != 1 && version != 2 && modeNames[version] == "" {
		http.Error(w, "supported schema versions are 1 and 2; incompatible modes are versions 3-9", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.schemaVersion = version
	var addedReport *volcanicReport
	if version != 1 {
		report := makeLiveReport(time.Now().UTC(), version)
		report.Variant = version
		c.reports = append(c.reports, report)
		if len(c.reports) > maxCatalogRecords {
			c.reports = append([]volcanicReport(nil), c.reports[len(c.reports)-maxCatalogRecords:]...)
		}
		addedReport = &report
	}
	c.mu.Unlock()
	response := map[string]any{"schema_version": version, "schema_mode": modeNameForVersion(version), "confidence_level_enabled": version >= 2}
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
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w, "invalid PVMBG credential", http.StatusUnauthorized)
			return
		}
		provided := parts[1]
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
		logger.Info("request complete", "service", service, "correlation_id", correlationID, "instance_id", r.Header.Get("X-Instance-ID"), "operation", r.URL.Path, "latency_ms", float64(time.Since(started).Microseconds())/1000, "status", status)
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

func checkHealth(endpoint string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}
