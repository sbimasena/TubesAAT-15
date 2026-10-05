package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/ingest"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type unavailableRepository struct{}

func (unavailableRepository) Upsert(context.Context, []domain.HazardEvent, string) ([]domain.HazardEvent, error) {
	return nil, errors.New("database offline")
}

func (unavailableRepository) List(context.Context, store.Filter, string) ([]domain.HazardEvent, error) {
	return nil, errors.New("database offline")
}

func (unavailableRepository) Ping(context.Context) error { return errors.New("database offline") }

func TestStorageOutageReturnsServiceUnavailable(t *testing.T) {
	repository := unavailableRepository{}
	manager := ingest.NewManager(nil, nil, repository, time.Second, nil)
	api := NewServer(repository, manager, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	for _, path := range []string{"/health", "/hazards", "/internal/v1/hazards"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-Correlation-ID", "test-storage-outage")
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("X-Correlation-ID") != "test-storage-outage" {
			t.Fatalf("%s: status=%d; expected 503 with correlation ID", path, response.Code)
		}
	}
}

// Stored hazards remain readable while upstreams have not completed a fresh poll.
type storedRepository struct{ unavailableRepository }

func (storedRepository) Ping(context.Context) error { return nil }
func (storedRepository) List(context.Context, store.Filter, string) ([]domain.HazardEvent, error) {
	return []domain.HazardEvent{{HazardID: "stored", Source: "PVMBG", HazardType: "VOLCANIC"}}, nil
}

func TestStoredDataIncludesFreshnessDuringUpstreamUnavailability(t *testing.T) {
	repository := storedRepository{}
	manager := ingest.NewManager(nil, nil, repository, time.Second, nil)
	api := NewServer(repository, manager, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	for _, path := range []string{"/health", "/hazards", "/internal/v1/hazards"} {
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: stored data must stay readable", path)
		}
		var body struct {
			Sources map[string]ingest.SourceStatus `json:"sources"`
			Data    []domain.HazardEvent           `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"BMKG", "PVMBG"} {
			status, ok := body.Sources[name]
			if !ok || status.Available || !status.Stale || status.StaleSince == nil || status.StaleAfterSeconds != 15 {
				t.Fatalf("%s: missing explicit source freshness: %+v", path, body.Sources)
			}
		}
		if path != "/health" && (len(body.Data) != 1 || body.Data[0].HazardID != "stored") {
			t.Fatalf("%s: stored data lost during upstream unavailability", path)
		}
	}
}
