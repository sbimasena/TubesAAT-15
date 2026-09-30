package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

const cacheLimit = 5000

type EventPublisher interface {
	Publish(context.Context, domain.HazardEvent) error
}

type LogPublisher struct {
	logger *slog.Logger
}

func NewLogPublisher(logger *slog.Logger) *LogPublisher {
	return &LogPublisher{logger: logger}
}

func (p *LogPublisher) Publish(_ context.Context, event domain.HazardEvent) error {
	if p.logger != nil {
		p.logger.Warn("broker publisher is not configured; event was not sent", "hazard_id", event.HazardID)
	}
	return nil
}

type SourceStatus struct {
	Available   bool       `json:"available"`
	LastSuccess *time.Time `json:"last_success_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

type Manager struct {
	bmkg      *source.BMKGClient
	pvmbg     *source.PVMBGClient
	repo      store.Repository
	publisher EventPublisher
	interval  time.Duration
	logger    *slog.Logger

	mu              sync.RWMutex
	eventCursor     time.Time
	warningCursor   time.Time
	reportCursor    time.Time
	eventsByID      map[string]domain.SeismicEvent
	warningsByEvent map[string]domain.TsunamiWarning
	status          map[string]SourceStatus
}

func NewManager(bmkg *source.BMKGClient, pvmbg *source.PVMBGClient, repo store.Repository, publisher EventPublisher, interval time.Duration, logger *slog.Logger) *Manager {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	return &Manager{
		bmkg:            bmkg,
		pvmbg:           pvmbg,
		repo:            repo,
		publisher:       publisher,
		interval:        interval,
		logger:          logger,
		eventsByID:      make(map[string]domain.SeismicEvent),
		warningsByEvent: make(map[string]domain.TsunamiWarning),
		status: map[string]SourceStatus{
			"BMKG":  {Available: false},
			"PVMBG": {Available: false},
		},
	}
}

func (m *Manager) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		m.runPoller(ctx, "BMKG", m.pollBMKG)
	}()
	go func() {
		defer workers.Done()
		m.runPoller(ctx, "PVMBG", m.pollPVMBG)
	}()
	workers.Wait()
}

func (m *Manager) Status() map[string]SourceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]SourceStatus, len(m.status))
	for name, status := range m.status {
		if status.LastSuccess != nil {
			copyOfTime := *status.LastSuccess
			status.LastSuccess = &copyOfTime
		}
		result[name] = status
	}
	return result
}

func (m *Manager) runPoller(ctx context.Context, name string, poll func(context.Context, string)) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	poll(ctx, correlationID(name))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll(ctx, correlationID(name))
		}
	}
}

func (m *Manager) pollBMKG(ctx context.Context, correlationID string) {
	started := time.Now()
	m.mu.RLock()
	eventSince := overlap(m.eventCursor)
	warningSince := overlap(m.warningCursor)
	m.mu.RUnlock()

	events, eventErr := m.bmkg.FetchSeismicEvents(ctx, eventSince, correlationID)
	warnings, warningErr := m.bmkg.FetchTsunamiWarnings(ctx, warningSince, correlationID)
	if eventErr != nil && warningErr != nil {
		m.setFailure("BMKG", correlationID, fmt.Errorf("seismic: %v; warnings: %w", eventErr, warningErr))
		return
	}
	if eventErr != nil {
		m.setFailure("BMKG", correlationID, eventErr)
	} else if warningErr != nil {
		m.setFailure("BMKG", correlationID, warningErr)
	} else {
		m.setSuccess("BMKG")
	}

	touched := make(map[string]struct{})
	m.mu.Lock()
	if eventErr == nil {
		for _, event := range events {
			m.eventsByID[event.EventID] = event
			if event.OccurredAt.After(m.eventCursor) {
				m.eventCursor = event.OccurredAt
			}
			touched[event.EventID] = struct{}{}
		}
	}
	if warningErr == nil {
		for _, warning := range warnings {
			previous, exists := m.warningsByEvent[warning.EventID]
			if !exists || warning.IssuedAt.After(previous.IssuedAt) {
				m.warningsByEvent[warning.EventID] = warning
				touched[warning.EventID] = struct{}{}
			}
			if warning.IssuedAt.After(m.warningCursor) {
				m.warningCursor = warning.IssuedAt
			}
		}
	}
	m.pruneBMKGCacheLocked()
	canonical := make([]domain.HazardEvent, 0, len(touched))
	for eventID := range touched {
		event, ok := m.eventsByID[eventID]
		if !ok {
			continue
		}
		warning, hasWarning := m.warningsByEvent[eventID]
		var warningPtr *domain.TsunamiWarning
		if hasWarning {
			warningPtr = &warning
		}
		canonical = append(canonical, domain.MapSeismic(event, warningPtr, time.Now().UTC()))
	}
	m.mu.Unlock()
	if len(canonical) == 0 {
		if m.logger != nil {
			m.logger.Info("BMKG poll complete", "correlation_id", correlationID, "events", 0, "latency_ms", elapsedMS(started))
		}
		return
	}
	m.persistAndPublish(ctx, "BMKG", correlationID, canonical)
}

func (m *Manager) pollPVMBG(ctx context.Context, correlationID string) {
	started := time.Now()
	m.mu.RLock()
	since := overlap(m.reportCursor)
	m.mu.RUnlock()

	reports, err := m.pvmbg.FetchVolcanicReports(ctx, since, correlationID)
	if err != nil {
		m.setFailure("PVMBG", correlationID, err)
		return
	}
	m.setSuccess("PVMBG")
	canonical := make([]domain.HazardEvent, 0, len(reports))
	for _, report := range reports {
		event, mapErr := domain.MapVolcanic(report, time.Now().UTC())
		if mapErr != nil {
			if m.logger != nil {
				m.logger.Warn("PVMBG report skipped", "correlation_id", correlationID, "source_ref_id", report.ReportID, "error", mapErr)
			}
			continue
		}
		canonical = append(canonical, event)
		if report.ReportedAt.After(since) {
			m.mu.Lock()
			if report.ReportedAt.After(m.reportCursor) {
				m.reportCursor = report.ReportedAt
			}
			m.mu.Unlock()
		}
	}
	if len(canonical) == 0 {
		if m.logger != nil {
			m.logger.Info("PVMBG poll complete", "correlation_id", correlationID, "events", 0, "latency_ms", elapsedMS(started))
		}
		return
	}
	m.persistAndPublish(ctx, "PVMBG", correlationID, canonical)
}

func (m *Manager) persistAndPublish(ctx context.Context, sourceName, correlationID string, events []domain.HazardEvent) {
	newEvents, err := m.repo.Upsert(ctx, events)
	if err != nil {
		if m.logger != nil {
			m.logger.Error("canonical store write failed", "source", sourceName, "correlation_id", correlationID, "error", err)
		}
		return
	}
	for _, event := range newEvents {
		if err := m.publisher.Publish(ctx, event); err != nil && m.logger != nil {
			m.logger.Error("event publish failed", "hazard_id", event.HazardID, "correlation_id", correlationID, "error", err)
		}
	}
	if m.logger != nil {
		m.logger.Info("canonical events ingested", "source", sourceName, "correlation_id", correlationID, "events", len(newEvents))
	}
}

func (m *Manager) setSuccess(sourceName string) {
	now := time.Now().UTC()
	m.mu.Lock()
	m.status[sourceName] = SourceStatus{Available: true, LastSuccess: &now}
	m.mu.Unlock()
}

func (m *Manager) setFailure(sourceName, correlationID string, err error) {
	m.mu.Lock()
	status := m.status[sourceName]
	status.Available = false
	status.LastError = err.Error()
	m.status[sourceName] = status
	m.mu.Unlock()
	if m.logger != nil {
		m.logger.Warn("source poll failed", "source", sourceName, "correlation_id", correlationID, "error", err)
	}
}

func (m *Manager) pruneBMKGCacheLocked() {
	if len(m.eventsByID) <= cacheLimit {
		return
	}
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	for id, event := range m.eventsByID {
		if event.OccurredAt.Before(cutoff) && len(m.eventsByID) > cacheLimit {
			delete(m.eventsByID, id)
			delete(m.warningsByEvent, id)
		}
	}
}

func overlap(cursor time.Time) time.Time {
	if cursor.IsZero() {
		return time.Time{}
	}
	return cursor.Add(-time.Minute)
}

func elapsedMS(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

func correlationID(sourceName string) string {
	return fmt.Sprintf("poll-%s-%d", sourceName, time.Now().UTC().UnixNano())
}
