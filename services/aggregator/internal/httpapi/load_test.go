package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/ingest"
)

type recordingRepository struct {
	storedRepository
	got *[]domain.HazardEvent
}

func (r recordingRepository) Upsert(_ context.Context, _ int64, events []domain.HazardEvent, _ string) ([]domain.HazardEvent, error) {
	*r.got = append(*r.got, events...)
	return events, nil
}

func TestLoadCatalog(t *testing.T) {
	var got []domain.HazardEvent
	repo := recordingRepository{got: &got}
	api := NewServer(repo, ingest.NewManager(nil, nil, repo, time.Second, nil), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	api.EnableAudit(fakeAudit{}, "secret")
	post := func(body, token string) int {
		request := httptest.NewRequest(http.MethodPost, "/internal/admin/load", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		return response.Code
	}
	ev := func(id string) string {
		return `{"event_id":"` + id + `","region_name":"Palu","epicenter_lat":-0.9,"epicenter_lon":119.8,"magnitude":5.1,"occurred_at":"2018-09-28T10:02:00Z"}`
	}
	for name, tc := range map[string]struct {
		body, token string
		want, n     int
	}{
		"ndjson":     {ev("a") + "\n" + ev("b") + "\n", "secret", 200, 2},
		"array":      {"\n [" + ev("c") + "]", "secret", 200, 1},
		"no token":   {ev("d"), "wrong", 401, 0},
		"empty":      {"  ", "secret", 400, 0},
		"missing id": {ev(""), "secret", 400, 0},
		"bad json":   {ev("e") + "\n{oops", "secret", 400, 0},
		"bad lat":    {strings.Replace(ev("f"), "-0.9", "-91", 1), "secret", 400, 0},
	} {
		got = nil
		if code := post(tc.body, tc.token); code != tc.want || len(got) != tc.n {
			t.Errorf("%s: status %d with %d events, want %d with %d", name, code, len(got), tc.want, tc.n)
		}
	}
	got = nil
	post(ev("g"), "secret")
	if got[0].Source != "BMKG" || got[0].HazardType != "SEISMIC" || got[0].SourceRefID != "g" || got[0].AreaName != "Palu" {
		t.Fatalf("not mapped like a polled BMKG event: %+v", got[0])
	}
}
