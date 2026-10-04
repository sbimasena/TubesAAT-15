package httpapi

import (
	"context"
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
