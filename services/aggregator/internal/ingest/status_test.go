package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

func TestStatusSnapshotExpiresAndPreservesFailureTime(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	lastIngested := now.Add(-20 * time.Second)
	failureAt := now.Add(-2 * time.Second)
	status := statusSnapshot(SourceStatus{Available: true, LastSuccess: &now,
		LastIngested: &lastIngested, StaleSince: &failureAt}, now, 15*time.Second)
	deadline := lastIngested.Add(15 * time.Second)
	if !status.Stale || !status.StaleSince.Equal(deadline) || status.StaleAfterSeconds != 15 {
		t.Fatalf("expired polling must be stale since its deadline: %+v", status)
	}
	// Returned timestamps must not allow a caller to mutate the manager's state.
	*status.LastIngested = now
	*status.LastSuccess = deadline
	if !lastIngested.Equal(now.Add(-20*time.Second)) || !now.Equal(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("status snapshot shares timestamps with live state")
	}
}

func TestFreshnessTracksCommitFailureAndRecovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]domain.VolcanicReport{{ReportID: "v1", VolcanoID: "V-001",
			AlertLevel: "SIAGA", ReportedAt: time.Now().UTC()}})
	}))
	defer server.Close()
	repo := &retryRepository{}
	manager := NewManager(nil, source.NewPVMBGClient(server.URL, "token", nil), repo, time.Second, nil)
	initial := manager.Status()["PVMBG"]
	if !initial.Stale || initial.StaleSince == nil || initial.LastIngested != nil {
		t.Fatal("a source must start stale until its first committed poll")
	}
	manager.pollPVMBG(context.Background(), "first-commit")
	fresh := manager.Status()["PVMBG"]
	if !fresh.Available || fresh.Stale || fresh.LastIngested == nil || fresh.StaleSince != nil {
		t.Fatalf("successful fetch and commit must mark the source fresh: %+v", fresh)
	}
	repo.fail = true
	manager.pollPVMBG(context.Background(), "failed-commit")
	failed := manager.Status()["PVMBG"]
	if !failed.Available || !failed.Stale || failed.IngestionError == "" || failed.StaleSince == nil ||
		!failed.LastIngested.Equal(*fresh.LastIngested) {
		t.Fatalf("a reachable source with failed storage must retain the last committed timestamp: %+v", failed)
	}
	repo.fail = false
	manager.pollPVMBG(context.Background(), "recovered-commit")
	recovered := manager.Status()["PVMBG"]
	if recovered.Stale || recovered.IngestionError != "" || recovered.StaleSince != nil ||
		!recovered.LastIngested.After(*fresh.LastIngested) {
		t.Fatalf("a committed retry must clear the ingestion failure: %+v", recovered)
	}
}

func TestIncompletePollCannotClearStaleStatus(t *testing.T) {
	for _, scenario := range []string{"empty-storage-failure", "invalid-volcano", "partial-bmkg", "unresolved-warning"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case scenario == "invalid-volcano":
					json.NewEncoder(w).Encode([]domain.VolcanicReport{{ReportID: "unknown", VolcanoID: "unknown"}})
				case scenario == "partial-bmkg" && r.URL.Path == "/tsunami-warnings":
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				case scenario == "partial-bmkg":
					json.NewEncoder(w).Encode([]domain.SeismicEvent{{EventID: "event", OccurredAt: time.Now().UTC()}})
				case scenario == "unresolved-warning" && r.URL.Path == "/tsunami-warnings":
					json.NewEncoder(w).Encode([]domain.TsunamiWarning{{WarningID: "warning", EventID: "missing"}})
				default:
					json.NewEncoder(w).Encode([]any{})
				}
			}))
			defer server.Close()
			repo := &retryRepository{fail: scenario == "empty-storage-failure"}
			manager := NewManager(source.NewBMKGClient(server.URL, "key", nil),
				source.NewPVMBGClient(server.URL, "token", nil), repo, time.Second, nil)
			name := "PVMBG"
			if scenario == "partial-bmkg" || scenario == "unresolved-warning" {
				name = "BMKG"
				manager.pollBMKG(context.Background(), "incomplete-bmkg")
			} else {
				manager.pollPVMBG(context.Background(), "incomplete-pvmbg")
			}
			status := manager.Status()[name]
			if !status.Stale || status.LastIngested != nil || status.StaleSince == nil {
				t.Fatalf("an incomplete poll must not advertise a fresh ingestion: %+v", status)
			}
			if scenario == "partial-bmkg" {
				if status.Available || status.LastError == "" || len(repo.events) != 1 {
					t.Fatal("the healthy BMKG endpoint should still persist while the source remains degraded")
				}
			} else if !status.Available || status.IngestionError == "" {
				t.Fatal("reachable sources must report their separate ingestion failure")
			}
		})
	}
}

func TestEmptySuccessfulPollIsFresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	}))
	defer server.Close()
	manager := NewManager(source.NewBMKGClient(server.URL, "key", nil),
		source.NewPVMBGClient(server.URL, "token", nil), &retryRepository{}, time.Second, nil)
	manager.pollBMKG(context.Background(), "empty-bmkg")
	manager.pollPVMBG(context.Background(), "empty-pvmbg")
	for name, status := range manager.Status() {
		if status.Stale || status.LastIngested == nil {
			t.Fatalf("%s: no new records does not mean a stale polling pipeline", name)
		}
	}
}

func TestSlowPVMBGDoesNotBlockBMKGPoller(t *testing.T) {
	var bmkgPolls atomic.Int32
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/volcanic-reports" {
			select {
			case <-blocked:
			case <-r.Context().Done():
				return
			}
		} else if r.URL.Path == "/seismic-events" {
			bmkgPolls.Add(1)
		}
		json.NewEncoder(w).Encode([]any{})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// This repository only validates commits; it holds no shared mutable data.
	manager := NewManager(source.NewBMKGClient(server.URL, "key", nil),
		source.NewPVMBGClient(server.URL, "token", nil), concurrentRepository{}, 20*time.Millisecond, nil)
	done := make(chan struct{})
	go func() { manager.Run(ctx); close(done) }()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for bmkgPolls.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			close(blocked)
			<-done
			t.Fatal("BMKG did not keep polling while PVMBG was blocked")
		case <-ticker.C:
		}
	}
	cancel()
	close(blocked)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollers did not stop after cancellation")
	}
}

type concurrentRepository struct{}

func (concurrentRepository) Upsert(context.Context, []domain.HazardEvent, string) ([]domain.HazardEvent, error) {
	return nil, nil
}
func (concurrentRepository) List(context.Context, store.Filter, string) ([]domain.HazardEvent, error) {
	return nil, nil
}
func (concurrentRepository) Ping(context.Context) error { return nil }
