package ingest

import "time"

// SourceStatus separates upstream reachability from a completed ingestion cycle.
// Freshness describes the polling pipeline, not the age of each hazard record.
type SourceStatus struct {
	Available         bool       `json:"available"`
	LastSuccess       *time.Time `json:"last_success_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	LastIngested      *time.Time `json:"last_ingested_at,omitempty"`
	IngestionError    string     `json:"ingestion_error,omitempty"`
	Stale             bool       `json:"stale"`
	StaleSince        *time.Time `json:"stale_since,omitempty"`
	StaleAfterSeconds float64    `json:"stale_after_seconds"`
}

func initialStatus(now time.Time) map[string]SourceStatus {
	return map[string]SourceStatus{
		"BMKG":  {Stale: true, StaleSince: &now},
		"PVMBG": {Stale: true, StaleSince: &now},
	}
}

func (m *Manager) Status() map[string]SourceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now().UTC()
	// Allow the normal polling interval plus upstream/storage deadlines before
	// treating a stalled worker as stale. Errors mark it stale immediately.
	staleAfter := max(3*m.interval, 15*time.Second)
	result := make(map[string]SourceStatus, len(m.status))
	for name, status := range m.status {
		result[name] = statusSnapshot(status, now, staleAfter)
	}
	return result
}

func statusSnapshot(status SourceStatus, now time.Time, staleAfter time.Duration) SourceStatus {
	status.LastSuccess = copyTime(status.LastSuccess)
	status.LastIngested = copyTime(status.LastIngested)
	status.StaleSince = copyTime(status.StaleSince)
	status.StaleAfterSeconds = staleAfter.Seconds()
	status.Stale = status.StaleSince != nil || status.LastIngested == nil
	if status.LastIngested != nil {
		deadline := status.LastIngested.Add(staleAfter)
		if !now.Before(deadline) {
			status.Stale = true
			if status.StaleSince == nil || deadline.Before(*status.StaleSince) {
				status.StaleSince = &deadline
			}
		}
	}
	return status
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (m *Manager) setSuccess(sourceName string) {
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status[sourceName]
	status.Available, status.LastSuccess, status.LastError = true, &now, ""
	m.status[sourceName] = status
}

func (m *Manager) setIngested(sourceName string) {
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status[sourceName]
	status.LastIngested, status.IngestionError = &now, ""
	status.Stale, status.StaleSince = false, nil
	m.status[sourceName] = status
}

func (m *Manager) setFailure(sourceName, correlationID string, err error) {
	m.mu.Lock()
	status := m.status[sourceName]
	status.Available, status.LastError = false, err.Error()
	markStale(&status)
	m.status[sourceName] = status
	m.mu.Unlock()
	if m.logger != nil {
		m.logger.Warn("source poll failed", "source", sourceName, "correlation_id", correlationID, "error", err)
	}
}

func (m *Manager) setIngestionFailure(sourceName, correlationID string, err error) {
	m.mu.Lock()
	status := m.status[sourceName]
	status.IngestionError = err.Error()
	markStale(&status)
	m.status[sourceName] = status
	m.mu.Unlock()
	if m.logger != nil {
		m.logger.Error("source ingestion failed", "source", sourceName, "correlation_id", correlationID, "error", err)
	}
}

func markStale(status *SourceStatus) {
	status.Stale = true
	if status.StaleSince == nil {
		now := time.Now().UTC()
		status.StaleSince = &now
	}
}
