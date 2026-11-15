package httpapi

import (
	"context"
	"encoding/json"
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

// fakeAudit answers like a router with store-2 down.
type fakeAudit struct{ store.Store }

func (fakeAudit) Query(_ context.Context, _ store.Filter, cursor string) (store.Page, error) {
	if cursor == "bad" {
		return store.Page{}, store.ErrInvalidCursor
	}
	wib := time.FixedZone("WIB", 7*3600)
	return store.Page{
		Items:            []domain.HazardEvent{{HazardID: "hz-1", OccurredAt: time.Date(2026, 11, 12, 9, 0, 0, 0, wib)}},
		UnavailableUnits: []string{"store-2"},
	}, nil
}

func (fakeAudit) Locate(_ context.Context, hazardID string) ([]store.Location, bool, error) {
	if hazardID == "hz-1" {
		return []store.Location{{Node: "node-a", Unit: "store-0", Role: "primary"}}, false, nil
	}
	return nil, false, nil
}

func TestAuditAPI(t *testing.T) {
	api := NewServer(storedRepository{}, ingest.NewManager(nil, nil, storedRepository{}, time.Second, nil),
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	api.EnableAudit(fakeAudit{}, "secret")
	handler := api.Handler()
	get := func(path, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	for path, want := range map[string]int{
		"/internal/audit/events?limit=0":          http.StatusBadRequest,
		"/internal/audit/events?cursor=bad":       http.StatusBadRequest,
		"/internal/audit/location":                http.StatusBadRequest,
		"/internal/audit/location?hazard_id=nope": http.StatusNotFound,
		"/internal/audit/location?hazard_id=hz-1": http.StatusOK,
	} {
		if got := get(path, "secret").Code; got != want {
			t.Errorf("%s: status %d, want %d", path, got, want)
		}
	}
	for _, token := range []string{"", "wrong"} {
		response := get("/internal/audit/events", token)
		var body struct{ Error struct{ Code string } }
		json.Unmarshal(response.Body.Bytes(), &body)
		if response.Code != http.StatusUnauthorized || body.Error.Code != "unauthorized" {
			t.Errorf("token %q: status %d body %s", token, response.Code, response.Body)
		}
	}

	var page struct {
		Items            []map[string]any `json:"items"`
		NextCursor       *string          `json:"next_cursor"`
		Partial          bool             `json:"partial"`
		UnavailableUnits []string         `json:"unavailable_units"`
	}
	response := get("/internal/audit/events", "secret")
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Items[0]["occurred_at"] != "2026-11-12T02:00:00Z" || page.NextCursor != nil || page.UnavailableUnits[0] != "store-2" {
		t.Fatalf("unexpected events body: %s", response.Body)
	}
}
