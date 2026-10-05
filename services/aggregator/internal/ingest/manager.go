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

type SourceStatus struct {
	Available   bool       `json:"available"`
	LastSuccess *time.Time `json:"last_success_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

type Manager struct {
	bmkg     *source.BMKGClient
	pvmbg    *source.PVMBGClient
	repo     store.Repository
	interval time.Duration
	logger   *slog.Logger

	mu              sync.RWMutex
	eventCursor     time.Time
	warningCursor   time.Time
	reportCursor    time.Time
	eventsByID      map[string]domain.SeismicEvent
	warningsByEvent map[string]domain.TsunamiWarning
	status          map[string]SourceStatus
}

func NewManager(bmkg *source.BMKGClient, pvmbg *source.PVMBGClient, repo store.Repository, interval time.Duration, logger *slog.Logger) *Manager {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	return &Manager{
		bmkg:            bmkg,
		pvmbg:           pvmbg,
		repo:            repo,
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
	eventCursor, warningCursor := m.eventCursor, m.warningCursor
	eventSince := overlap(eventCursor)
	warningSince := overlap(warningCursor)
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
			if event.OccurredAt.After(eventCursor) {
				eventCursor = event.OccurredAt
			}
			touched[event.EventID] = struct{}{}
		}
	}
	if warningErr == nil {
		for _, warning := range warnings {
			previous, exists := m.warningsByEvent[warning.EventID]
			if !exists || warning.IssuedAt.After(previous.IssuedAt) {
				m.warningsByEvent[warning.EventID] = warning
			}
			// Retry cached warnings too: the preceding database transaction may have failed.
			touched[warning.EventID] = struct{}{}
			if warning.IssuedAt.After(warningCursor) {
				warningCursor = warning.IssuedAt
			}
		}
	}
	m.pruneBMKGCacheLocked()
	canonical := make([]domain.HazardEvent, 0, len(touched))
	for eventID := range touched {
		event, ok := m.eventsByID[eventID]
		if !ok {
			// Keep unresolved warnings in the polling window until their event can be stored.
			warningCursor = m.warningCursor
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
	if err := m.persist(ctx, "BMKG", correlationID, canonical); err != nil {
		return
	}
	m.mu.Lock()
	m.eventCursor, m.warningCursor = eventCursor, warningCursor
	m.mu.Unlock()
}

func (m *Manager) pollPVMBG(ctx context.Context, correlationID string) {
	started := time.Now()
	m.mu.RLock()
	reportCursor := m.reportCursor
	since := overlap(reportCursor)
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
		if report.ReportedAt.After(reportCursor) {
			reportCursor = report.ReportedAt
		}
	}
	if len(canonical) == 0 {
		if m.logger != nil {
			m.logger.Info("PVMBG poll complete", "correlation_id", correlationID, "events", 0, "latency_ms", elapsedMS(started))
		}
		return
	}
	if err := m.persist(ctx, "PVMBG", correlationID, canonical); err != nil {
		return
	}
	m.mu.Lock()
	m.reportCursor = reportCursor
	m.mu.Unlock()
}

func (m *Manager) persist(ctx context.Context, sourceName, correlationID string, events []domain.HazardEvent) error {
	changedEvents, err := m.repo.Upsert(ctx, events, correlationID)
	if err != nil {
		if m.logger != nil {
			m.logger.Error("canonical store write failed", "source", sourceName, "correlation_id", correlationID, "error", err)
		}
		return err
	}
	if m.logger != nil {
		m.logger.Info("canonical events and outbox committed", "source", sourceName, "correlation_id", correlationID, "events", len(changedEvents))
	}
	return nil
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
