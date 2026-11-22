package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

const cacheLimit = 5000

type Manager struct {
	bmkg     *source.BMKGClient
	pvmbg    *source.PVMBGClient
	repo     store.Repository
	interval time.Duration
	logger   *slog.Logger
	epoch    int64
	verify   func(context.Context) error
	cursors  cursorStore
	pvmbgDLQ pvmbgDeadletter

	mu              sync.RWMutex
	eventCursor     time.Time
	warningCursor   time.Time
	reportCursor    time.Time
	eventsByID      map[string]domain.SeismicEvent
	warningsByEvent map[string]domain.TsunamiWarning
	status          map[string]SourceStatus
	statusProviders map[string]func() SourceStatus
}

func NewManager(bmkg *source.BMKGClient, pvmbg *source.PVMBGClient, repo store.Repository, interval time.Duration, logger *slog.Logger) *Manager {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	m := &Manager{
		bmkg:            bmkg,
		pvmbg:           pvmbg,
		repo:            repo,
		interval:        interval,
		logger:          logger,
		eventsByID:      make(map[string]domain.SeismicEvent),
		warningsByEvent: make(map[string]domain.TsunamiWarning),
		status:          initialStatus(time.Now().UTC()),
	}
	// The replicated store (store.Router) parks PVMBG records; test fakes may not.
	if dlq, ok := repo.(pvmbgDeadletter); ok {
		m.pvmbgDLQ = dlq
	}
	return m
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
		m.runPoller(ctx, "PVMBG", func(ctx context.Context, id string) {
			m.pollPVMBG(ctx, id)
			if err := m.retryPVMBG(ctx, id); err != nil && ctx.Err() == nil && m.logger != nil {
				m.logger.Warn("PVMBG replay failed", "correlation_id", id, "error", err)
			}
		})
	}()
	workers.Wait()
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
	if m.verify != nil {
		ctx = source.WithLeaseVerifier(ctx, m.verify)
	}
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
	now := time.Now().UTC()
	canonical := make([]domain.HazardEvent, 0, len(touched))
	unresolvedWarning := false
	for eventID := range touched {
		event, ok := m.eventsByID[eventID]
		if !ok {
			// A warning may legitimately arrive before its seismic event. Keep
			// retrying only recent warnings; drop stale unmatched warnings
			// instead of failing every poll forever (clock skew / lost events).
			if warning, hasWarning := m.warningsByEvent[eventID]; hasWarning &&
				!warning.IssuedAt.IsZero() && warning.IssuedAt.Before(now.Add(-checkpointWindow)) {
				delete(m.warningsByEvent, eventID)
				continue
			}
			// Keep unresolved warnings in the polling window until their event can be stored.
			unresolvedWarning = true
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
	// A skewed clock (upstream, or ours during a skew) must not leave cursors beyond
	// local time, otherwise recent events fall outside the next polling window and
	// disappear. Capped after the loop so the unresolved-warning fallback is capped too.
	if eventCursor.After(now) {
		eventCursor = now
	}
	if warningCursor.After(now) {
		warningCursor = now
	}
	m.mu.Unlock()
	if unresolvedWarning {
		m.setIngestionFailure("BMKG", correlationID, fmt.Errorf("tsunami warning has no matching seismic event"))
	}
	if err := m.persist(ctx, "BMKG", correlationID, canonical); err != nil {
		return
	}
	if len(canonical) == 0 {
		if m.logger != nil {
			m.logger.Info("BMKG poll complete", "correlation_id", correlationID, "events", 0, "latency_ms", elapsedMS(started))
		}
	}
	if eventErr == nil && warningErr == nil && !unresolvedWarning {
		m.setIngested("BMKG")
	}
	recentEvents, recentWarnings := recentCorrelation(m.eventsByID, m.warningsByEvent, time.Now().UTC())
	if err := m.saveCheckpoint(ctx, "BMKG", checkpoint{EventCursor: eventCursor, WarningCursor: warningCursor, Events: recentEvents, Warnings: recentWarnings}); err != nil {
		m.setIngestionFailure("BMKG", correlationID, err)
		return
	}
	m.mu.Lock()
	m.eventCursor, m.warningCursor = eventCursor, warningCursor
	m.mu.Unlock()
}

func (m *Manager) pollPVMBG(ctx context.Context, correlationID string) {
	if m.verify != nil {
		ctx = source.WithLeaseVerifier(ctx, m.verify)
	}
	started := time.Now()
	m.mu.RLock()
	reportCursor := m.reportCursor
	since := overlap(reportCursor)
	m.mu.RUnlock()

	batch, err := m.pvmbg.FetchVolcanicBatch(ctx, since, correlationID)
	if err != nil {
		m.setFailure("PVMBG", correlationID, err)
		return
	}
	m.setSuccess("PVMBG")
	if batch.Invalid != nil {
		err := batch.Invalid
		// An oversized capture is only a prefix, not a durable copy of the message.
		if !batch.Truncated {
			if parkErr := m.parkPVMBG(ctx, batch.Raw, "", since, err); parkErr != nil {
				err = parkErr
			}
		}
		m.setIngestionFailure("PVMBG", correlationID, err)
		return // No trustworthy next position for a broken envelope.
	}
	canonical := make([]domain.HazardEvent, 0, len(batch.Records))
	validRecords := make([]source.PVMBGRecord, 0, len(batch.Records))
	var parkFailure, recordFailure error
	for _, record := range batch.Records {
		event, err := mapPVMBGRecord(record)
		if err != nil {
			recordFailure = err
			if parkErr := m.parkPVMBG(ctx, record.Raw, record.Report.ReportID, since, err); parkErr != nil {
				parkFailure = parkErr
			} else if record.Report.ReportedAt.After(reportCursor) {
				reportCursor = record.Report.ReportedAt
			}
			continue
		}
		canonical = append(canonical, event)
		validRecords = append(validRecords, record)
		if record.Report.ReportedAt.After(reportCursor) {
			reportCursor = record.Report.ReportedAt
		}
	}
	if parkFailure != nil {
		m.setIngestionFailure("PVMBG", correlationID, parkFailure)
	} else if recordFailure != nil {
		m.setIngestionFailure("PVMBG", correlationID, recordFailure)
	}
	if err := m.persist(ctx, "PVMBG", correlationID, canonical); err != nil {
		// Keep the checkpoint for a normal repoll even if the DLQ is also
		// unavailable. A partial canonical write is safe to retry by ID.
		if _, durableRetry := m.pvmbgDLQ.(pvmbgReplayStore); durableRetry && ctx.Err() == nil &&
			deadletter.Retryable(pvmbgReason(err)) && !errors.Is(err, store.ErrFenced) {
			for _, record := range validRecords {
				if err := m.parkPVMBG(ctx, record.Raw, record.Report.ReportID, since, err); err != nil {
					if m.logger != nil {
						m.logger.Warn("PVMBG retry could not be scheduled; checkpoint retained", "correlation_id", correlationID, "error", err)
					}
					break
				}
			}
		}
		return
	}
	if len(canonical) == 0 {
		if m.logger != nil {
			m.logger.Info("PVMBG poll complete", "correlation_id", correlationID, "events", 0, "latency_ms", elapsedMS(started))
		}
	}
	if parkFailure != nil {
		return
	}
	if recordFailure == nil {
		m.setIngested("PVMBG")
	}
	if err := m.saveCheckpoint(ctx, "PVMBG", checkpoint{ReportCursor: reportCursor}); err != nil {
		m.setIngestionFailure("PVMBG", correlationID, err)
		return
	}
	m.mu.Lock()
	m.reportCursor = reportCursor
	m.mu.Unlock()
}

func (m *Manager) persist(ctx context.Context, sourceName, correlationID string, events []domain.HazardEvent) error {
	if m.verify != nil {
		if err := m.verify(ctx); err != nil {
			return err
		}
	}
	changedEvents, err := m.repo.Upsert(ctx, m.epoch, events, correlationID)
	if err != nil {
		m.setIngestionFailure(sourceName, correlationID, err)
		return err
	}
	if m.logger != nil {
		m.logger.Info("canonical events and outbox committed", "source", sourceName, "correlation_id", correlationID, "events", len(changedEvents))
	}
	return nil
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

// checkpointWindow bounds the correlation state written every poll and read by every
// follower. The full cache stays in memory on the leader.
// shortcut: assumes a warning follows its event within an hour (mock BMKG: 1–2 min); widen if real feeds send later updates.
const checkpointWindow = time.Hour

// recentCorrelation keeps events from the last checkpointWindow with their warnings,
// plus warnings whose event is not cached yet, so they stay retryable after failover.
func recentCorrelation(events map[string]domain.SeismicEvent, warnings map[string]domain.TsunamiWarning, now time.Time) (map[string]domain.SeismicEvent, map[string]domain.TsunamiWarning) {
	cutoff := now.Add(-checkpointWindow)
	keptEvents := make(map[string]domain.SeismicEvent)
	keptWarnings := make(map[string]domain.TsunamiWarning)
	for id, event := range events {
		if event.OccurredAt.Before(cutoff) {
			continue
		}
		keptEvents[id] = event
		if warning, ok := warnings[id]; ok {
			keptWarnings[id] = warning
		}
	}
	for id, warning := range warnings {
		if _, cached := events[id]; cached {
			continue
		}
		// Bound retries for warnings whose event never arrives: drop warnings
		// older than the checkpoint window instead of carrying them forever.
		if !warning.IssuedAt.IsZero() && warning.IssuedAt.Before(cutoff) {
			continue
		}
		keptWarnings[id] = warning
	}
	return keptEvents, keptWarnings
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

type cursorStore interface {
	SaveCursor(context.Context, int64, string, string) error
	LoadCursor(context.Context, string) (string, error)
}

// A BMKG checkpoint includes correlation state: late warnings may refer to an
// event older than the polling overlap after failover.
type checkpoint struct {
	Status        *SourceStatus
	EventCursor   time.Time
	WarningCursor time.Time
	ReportCursor  time.Time
	Events        map[string]domain.SeismicEvent
	Warnings      map[string]domain.TsunamiWarning
}

// PrepareTerm is called with workers stopped, after the storage fence barrier.
func (m *Manager) PrepareTerm(ctx context.Context, epoch int64, verify func(context.Context) error, cursors cursorStore) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epoch, m.verify, m.cursors = epoch, verify, cursors
	for _, source := range []string{"BMKG", "PVMBG"} {
		raw, err := cursors.LoadCursor(ctx, source)
		if err != nil {
			return err
		}
		if raw == "" {
			continue
		}
		var c checkpoint
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return fmt.Errorf("load %s checkpoint: %w", source, err)
		}
		if c.Status != nil {
			m.status[source] = *c.Status
		}
		if source == "BMKG" {
			// A checkpoint written before cursor capping may carry a future
			// cursor from a skewed upstream clock; never restore it verbatim.
			eventCursor, warningCursor := c.EventCursor, c.WarningCursor
			now := time.Now().UTC()
			if eventCursor.After(now) {
				eventCursor = now
			}
			if warningCursor.After(now) {
				warningCursor = now
			}
			m.eventCursor, m.warningCursor = eventCursor, warningCursor
			if c.Events != nil {
				m.eventsByID = c.Events
			}
			if c.Warnings != nil {
				m.warningsByEvent = c.Warnings
			}
		} else {
			m.reportCursor = c.ReportCursor
		}
	}
	return nil
}

func (m *Manager) saveCheckpoint(ctx context.Context, source string, c checkpoint) error {
	if m.cursors == nil {
		return nil
	} // M1 test repositories have no durable cursor API.
	if err := m.verify(ctx); err != nil {
		return err
	}
	m.mu.RLock()
	status := m.status[source]
	m.mu.RUnlock()
	c.Status = &status
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return m.cursors.SaveCursor(ctx, m.epoch, source, string(raw))
}

// RunStatusRefresh lets read-only followers report the last durable ingestion
// status. Freshness still expires locally; followers never poll upstreams.
func (m *Manager) RunStatusRefresh(ctx context.Context, cursors cursorStore, isLeader func() bool) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		if !isLeader() {
			for _, source := range []string{"BMKG", "PVMBG"} {
				raw, err := cursors.LoadCursor(ctx, source)
				if err != nil {
					continue
				}
				var c checkpoint
				if json.Unmarshal([]byte(raw), &c) != nil || c.Status == nil {
					continue
				}
				m.mu.Lock()
				if !isLeader() {
					m.status[source] = *c.Status
				}
				m.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
