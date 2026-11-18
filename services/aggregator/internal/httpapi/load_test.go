package httpapi

import (
	"context"
	"errors"
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

func TestLoadUsesCurrentTermAndRejectsFollowers(t *testing.T) {
	repo := &epochRepository{}
	api := NewServer(repo, ingest.NewManager(nil, nil, repo, time.Second, nil), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	api.EnableAudit(fakeAudit{}, "secret")
	active := true
	api.EnableWriteTerm(func(context.Context) (int64, error) {
		if !active {
			return 0, errors.New("not leader")
		}
		return 17, nil
	})
	body := `[{"event_id":"a","region_name":"Palu","epicenter_lat":-0.9,"epicenter_lon":119.8,"occurred_at":"2018-09-28T10:02:00Z"}]`
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/admin/load", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, req)
		return response
	}
	if res := post(); res.Code != 200 || repo.epoch != 17 {
		t.Fatalf("status=%d epoch=%d", res.Code, repo.epoch)
	}
	active = false
	if res := post(); res.Code != 503 || res.Header().Get("Retry-After") != "1" || repo.calls != 1 {
		t.Fatalf("status=%d calls=%d", res.Code, repo.calls)
	}
}

type epochRepository struct {
	storedRepository
	epoch int64
	calls int
}

func (r *epochRepository) Upsert(_ context.Context, epoch int64, events []domain.HazardEvent, _ string) ([]domain.HazardEvent, error) {
	r.epoch = epoch
	r.calls++
	return events, nil
}
