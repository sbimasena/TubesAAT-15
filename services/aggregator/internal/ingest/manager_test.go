package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type retryRepository struct {
	fail   bool
	events []domain.HazardEvent
}

func (r *retryRepository) Upsert(_ context.Context, events []domain.HazardEvent, correlationID string) ([]domain.HazardEvent, error) {
	if r.fail {
		return nil, errors.New("simulated write failure")
	}
	if correlationID == "" {
		return nil, errors.New("missing correlation ID")
	}
	r.events = append([]domain.HazardEvent(nil), events...)
	return events, nil
}

func (r *retryRepository) List(context.Context, store.Filter, string) ([]domain.HazardEvent, error) {
	return r.events, nil
}

func (r *retryRepository) Ping(context.Context) error { return nil }

func TestPollersRetryAfterPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	var lateWarning atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Correlation-ID") == "" {
			t.Error("poll must propagate a correlation ID")
		}
		switch r.URL.Path {
		case "/seismic-events":
			events := []domain.SeismicEvent{
				{EventID: "old", RegionName: "Old Area", Magnitude: 5, OccurredAt: now.Add(-10 * time.Minute)},
				{EventID: "new", RegionName: "New Area", Magnitude: 4, OccurredAt: now},
			}
			since, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("since"))
			if !since.IsZero() {
				events = events[1:]
			}
			json.NewEncoder(w).Encode(events)
		case "/tsunami-warnings":
			warnings := []domain.TsunamiWarning{}
			if lateWarning.Load() {
				warnings = append(warnings, domain.TsunamiWarning{WarningID: "late", EventID: "old",
					ThreatLevel: "AWAS", IssuedAt: now.Add(time.Minute)})
			}
			json.NewEncoder(w).Encode(warnings)
		case "/volcanic-reports":
			json.NewEncoder(w).Encode([]domain.VolcanicReport{{ReportID: "report", VolcanoID: "V-001",
				AlertLevel: "SIAGA", ReportedAt: now}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	repo := &retryRepository{fail: true}
	manager := NewManager(source.NewBMKGClient(server.URL, "bmkg-key", nil),
		source.NewPVMBGClient(server.URL, "pvmbg-token", nil), repo, time.Second, nil)
	manager.pollBMKG(ctx, "test-bmkg-failure")
	manager.pollPVMBG(ctx, "test-pvmbg-failure")
	if !manager.eventCursor.IsZero() || !manager.warningCursor.IsZero() || !manager.reportCursor.IsZero() {
		t.Fatal("failed persistence must not advance any cursor")
	}
	repo.fail = false
	manager.pollBMKG(ctx, "test-bmkg-recovery")
	manager.pollPVMBG(ctx, "test-pvmbg-recovery")
	if !manager.eventCursor.Equal(now) || !manager.reportCursor.Equal(now) {
		t.Fatal("cursors must advance after successful persistence")
	}
	lateWarning.Store(true)
	repo.fail = true
	manager.pollBMKG(ctx, "test-warning-failure")
	if !manager.warningCursor.IsZero() {
		t.Fatal("failed warning update must not advance warning cursor")
	}
	repo.fail = false
	manager.pollBMKG(ctx, "test-warning-retry")
	found := false
	for _, event := range repo.events {
		if event.SourceRefID == "old" && event.Severity == "AWAS" {
			found = true
		}
	}
	if !found || !manager.warningCursor.Equal(now.Add(time.Minute)) {
		t.Fatal("a cached late warning must be retried after the write failure")
	}
}
