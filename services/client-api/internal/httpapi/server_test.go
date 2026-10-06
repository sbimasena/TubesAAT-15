package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/authclient"
)

func TestHealthGeneratesOrPreservesCorrelationID(t *testing.T) {
	handler := NewHandler(nil, nil, false, 64, nil)
	for _, incoming := range []string{"", "request-456"} {
		request := httptest.NewRequest(http.MethodGet, "/health", nil)
		if incoming != "" {
			request.Header.Set("X-Correlation-ID", incoming)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		id := response.Header().Get("X-Correlation-ID")
		if response.Code != http.StatusOK || id == "" || (incoming != "" && id != incoming) {
			t.Fatalf("unexpected health result: status=%d id=%q", response.Code, id)
		}
	}
}

type testValidator struct {
	scope string
	err   error
	calls int
}

func (v *testValidator) Validate(_ context.Context, access, correlation string) (authclient.Identity, error) {
	v.calls++
	if access != "test-access" || correlation == "" {
		return authclient.Identity{}, authclient.ErrInvalid
	}
	return authclient.Identity{ClientID: v.scope, Scope: v.scope, SessionID: "sid"}, v.err
}

type testLister struct{ calls int }

const emptyResponse = `{"data":[],"count":0,"sources":{"BMKG":{"available":true,"stale":false,"last_ingested_at":"2026-10-06T00:00:00Z","stale_after_seconds":15},"PVMBG":{"available":false,"stale":true,"stale_after_seconds":15}}}`

func (l *testLister) List(_ context.Context, _ url.Values, _ string) ([]byte, error) {
	l.calls++
	return []byte(`{"count":1,"sources":{"BMKG":{"available":true,"stale":false,"last_ingested_at":"2026-10-06T00:00:01Z","stale_after_seconds":15,"last_error":"private diagnostic","ingestion_error":"private mapping","last_success_at":"2026-10-06T00:00:02Z","future_raw":"private metadata"},"PVMBG":{"available":false,"stale":true,"stale_since":"2026-10-06T00:00:02Z","stale_after_seconds":15,"last_error":"private outage"}},"data":[{"hazard_id":"h","source":"BMKG","source_ref_id":"private-ref","hazard_type":"SEISMIC","severity":"SIAGA","area_name":"Bandung","latitude":1,"longitude":2,"occurred_at":"2026-10-06T00:00:00Z","ingested_at":"2026-10-06T00:00:01Z","attributes":{"private":"raw"},"future_raw":"private"}]}`), nil
}

func TestProtectedHazardsEnforceScopesBeforeFetchingData(t *testing.T) {
	for _, test := range []struct {
		scope, query        string
		want, fields, calls int
	}{
		{"media", "", 200, 7, 1}, {"field-team", "", 200, 11, 1}, {"internal-ops", "", 200, 11, 1},
		{"media", "?include_raw=true", 403, 0, 0}, {"media", "?fields=attributes", 403, 0, 0},
		{"media", "?fields=source_ref_id", 403, 0, 0}, {"media", "?fields=latitude", 403, 0, 0},
		{"media", "?fields=ingested_at", 200, 1, 1}, {"media", "?fields=unknown", 400, 0, 0},
		{"media", "?fields=source&fields=attributes", 400, 0, 0},
		{"media", "?scope=internal-ops&include_raw=true", 403, 0, 0},
	} {
		validator, lister := &testValidator{scope: test.scope}, &testLister{}
		var logs bytes.Buffer
		handler := NewHandler(lister, validator, false, 64, slog.New(slog.NewJSONHandler(&logs, nil)))
		r := httptest.NewRequest("GET", "/hazards"+test.query, nil)
		r.Header.Set("Authorization", "Bearer test-access")
		r.Header.Set("X-Correlation-ID", "protected-flow")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.want || lister.calls != test.calls {
			t.Fatalf("%s %s: status=%d calls=%d", test.scope, test.query, w.Code, lister.calls)
		}
		if test.want == 200 {
			var payload struct {
				Data    []map[string]any          `json:"data"`
				Count   int                       `json:"count"`
				Sources map[string]map[string]any `json:"sources"`
			}
			if json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload.Count != 1 || len(payload.Data[0]) != test.fields {
				t.Fatal("unexpected projection/count")
			}
			if len(payload.Sources) != 2 || len(payload.Sources["BMKG"]) != 5 || len(payload.Sources["PVMBG"]) != 5 ||
				strings.Contains(w.Body.String(), "private diagnostic") || strings.Contains(w.Body.String(), "private mapping") ||
				strings.Contains(w.Body.String(), "private metadata") || strings.Contains(w.Body.String(), "private outage") ||
				strings.Contains(w.Body.String(), "last_success_at") || strings.Contains(w.Body.String(), "future_raw") {
				t.Fatal("source metadata or future field leaked")
			}
		}
		if strings.Contains(logs.String(), "test-access") || !strings.Contains(logs.String(), `"client_id":"`+test.scope+`"`) {
			t.Fatal("unsafe/missing identity logs")
		}
	}
}

func TestProtectedHazardsRejectTokensAndFailClosedOnAuthOutage(t *testing.T) {
	for _, test := range []struct {
		header string
		err    error
		want   int
	}{
		{"", nil, 401}, {"Basic test-access", nil, 401}, {"Bearer", nil, 401}, {"Bearer x y", nil, 401},
		{"Bearer test-access", authclient.ErrInvalid, 401}, {"Bearer test-access", authclient.ErrUnavailable, 503},
	} {
		validator, lister := &testValidator{scope: "media", err: test.err}, &testLister{}
		r := httptest.NewRequest("GET", "/hazards", nil)
		if test.header != "" {
			r.Header.Set("Authorization", test.header)
		}
		r.Header.Set("X-Correlation-ID", "protected-flow")
		w := httptest.NewRecorder()
		NewHandler(lister, validator, false, 64, nil).ServeHTTP(w, r)
		if w.Code != test.want || lister.calls != 0 {
			t.Fatal("invalid/unverified token reached Aggregator")
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("authorization failure may be cached")
		}
	}
}

func TestProvisionalHazardEndpointIsDisabledByDefault(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler(nil, nil, false, 64, nil).ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/internal/provisional/hazards", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

func TestProvisionalHazardEndpointCallsAggregator(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/hazards" || r.URL.Query().Get("source") != "BMKG" || r.Header.Get("X-Correlation-ID") != "flow-123" {
			t.Errorf("unexpected Aggregator request: %s / %q", r.URL.String(), r.Header.Get("X-Correlation-ID"))
		}
		_, _ = w.Write([]byte(emptyResponse))
	}))
	defer upstream.Close()
	client, err := aggregator.NewClient(upstream.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/provisional/hazards?source=BMKG", nil)
	request.Header.Set("X-Correlation-ID", "flow-123")
	request.Header.Set("Authorization", "Bearer test-access")
	response := httptest.NewRecorder()
	NewHandler(client, &testValidator{scope: "internal-ops"}, true, 64, nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Provisional-Endpoint") != "true" || response.Header().Get("X-Correlation-ID") != "flow-123" {
		t.Fatalf("unexpected proxy result: status=%d headers=%v", response.Code, response.Header())
	}
	var body struct {
		Data    []json.RawMessage         `json:"data"`
		Count   int                       `json:"count"`
		Sources map[string]map[string]any `json:"sources"`
	}
	if json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Data == nil || body.Count != 0 ||
		len(body.Sources) != 2 || body.Sources["PVMBG"]["last_ingested_at"] != nil || body.Sources["PVMBG"]["stale"] != true {
		t.Fatal("legacy route lost projected source status or explicit nulls")
	}
}

func TestProvisionalHazardEndpointHandlesDependencyFailures(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		upstreamStatus int
		payload        string
		wait           bool
		flush          bool
		closed         bool
		wantStatus     int
		wantCode       string
	}{
		{name: "connection refused", closed: true, wantStatus: http.StatusBadGateway, wantCode: "aggregator_error"},
		{name: "upstream unavailable", upstreamStatus: http.StatusServiceUnavailable, payload: "internal upstream detail", wantStatus: http.StatusServiceUnavailable, wantCode: "aggregator_unavailable"},
		{name: "invalid filter", upstreamStatus: http.StatusBadRequest, payload: "internal filter detail", wantStatus: http.StatusBadRequest, wantCode: "invalid_query"},
		{name: "upstream error", upstreamStatus: http.StatusInternalServerError, payload: "internal stack trace", wantStatus: http.StatusBadGateway, wantCode: "aggregator_error"},
		{name: "malformed response", payload: "not JSON", wantStatus: http.StatusBadGateway, wantCode: "invalid_aggregator_response"},
		{name: "header timeout", wait: true, wantStatus: http.StatusGatewayTimeout, wantCode: "aggregator_timeout"},
		{name: "body timeout", payload: `{"data":`, flush: true, wait: true, wantStatus: http.StatusGatewayTimeout, wantCode: "aggregator_timeout"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario.upstreamStatus != 0 {
					w.WriteHeader(scenario.upstreamStatus)
				}
				if scenario.payload != "" {
					_, _ = w.Write([]byte(scenario.payload))
				}
				if scenario.flush {
					w.(http.Flusher).Flush()
				}
				if scenario.wait {
					<-r.Context().Done()
				}
			}))
			defer upstream.Close()
			if scenario.closed {
				upstream.Close()
			}
			client, err := aggregator.NewClient(upstream.URL, 50*time.Millisecond, nil)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/internal/provisional/hazards", nil)
			request.Header.Set("X-Correlation-ID", "failure-test")
			request.Header.Set("Authorization", "Bearer test-access")
			response := httptest.NewRecorder()
			NewHandler(client, &testValidator{scope: "internal-ops"}, true, 64, nil).ServeHTTP(response, request)
			assertAPIError(t, response, scenario.wantStatus, scenario.wantCode, "failure-test")
			if strings.Contains(response.Body.String(), "internal") || strings.Contains(response.Body.String(), upstream.URL) {
				t.Fatal("dependency details leaked")
			}
			if response.Header().Get("X-Correlation-ID") != "failure-test" {
				t.Fatal("failure response lost correlation ID")
			}
		})
	}
}

func TestLegacyAliasCannotBypassAuthenticationOrMediaProjection(t *testing.T) {
	lister := &testLister{}
	handler := NewHandler(lister, &testValidator{scope: "media"}, true, 64, nil)
	for _, test := range []struct {
		authorization, query string
		want                 int
	}{
		{"", "", 401}, {"Bearer test-access", "?include_raw=true", 403},
		{"Bearer test-access", "?fields=attributes", 403}, {"Bearer test-access", "", 200},
	} {
		r := httptest.NewRequest("GET", "/internal/provisional/hazards"+test.query, nil)
		if test.authorization != "" {
			r.Header.Set("Authorization", test.authorization)
		}
		r.Header.Set("X-Correlation-ID", "legacy-check")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatalf("legacy bypass: status=%d", w.Code)
		}
		if test.want == 200 {
			var payload struct {
				Data []map[string]any `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &payload) != nil || len(payload.Data[0]) != 7 {
				t.Fatal("legacy raw fields leaked")
			}
		}
	}
	if lister.calls != 1 {
		t.Fatal("denied legacy request reached Aggregator")
	}
}
