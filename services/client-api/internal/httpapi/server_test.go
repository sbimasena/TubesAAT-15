package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
)

func TestHealthGeneratesOrPreservesCorrelationID(t *testing.T) {
	handler := NewHandler(nil, false, nil)
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

func TestProvisionalHazardEndpointIsDisabledByDefault(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler(nil, false, nil).ServeHTTP(response,
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
		_, _ = w.Write([]byte(`{"data":[],"count":0,"sources":{"BMKG":{"available":true}}}`))
	}))
	defer upstream.Close()
	client, err := aggregator.NewClient(upstream.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/provisional/hazards?source=BMKG", nil)
	request.Header.Set("X-Correlation-ID", "flow-123")
	response := httptest.NewRecorder()
	NewHandler(client, true, nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Provisional-Endpoint") != "true" || response.Header().Get("X-Correlation-ID") != "flow-123" {
		t.Fatalf("unexpected proxy result: status=%d headers=%v", response.Code, response.Header())
	}
	if response.Body.String() != `{"data":[],"count":0,"sources":{"BMKG":{"available":true}}}` {
		t.Fatalf("unexpected body: %s", response.Body.String())
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
	}{
		{name: "connection refused", closed: true, wantStatus: http.StatusBadGateway},
		{name: "upstream unavailable", upstreamStatus: http.StatusServiceUnavailable, payload: "internal upstream detail", wantStatus: http.StatusBadGateway},
		{name: "malformed response", payload: "not JSON", wantStatus: http.StatusBadGateway},
		{name: "header timeout", wait: true, wantStatus: http.StatusGatewayTimeout},
		{name: "body timeout", payload: `{"data":`, flush: true, wait: true, wantStatus: http.StatusGatewayTimeout},
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
			response := httptest.NewRecorder()
			NewHandler(client, true, nil).ServeHTTP(response, request)
			if response.Code != scenario.wantStatus || response.Body.String() != "Aggregator request failed\n" {
				t.Fatalf("unexpected failure response: status=%d body=%q", response.Code, response.Body.String())
			}
			if response.Header().Get("X-Correlation-ID") != "failure-test" {
				t.Fatal("failure response lost correlation ID")
			}
		})
	}
}
