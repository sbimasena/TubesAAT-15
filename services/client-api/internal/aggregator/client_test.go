package aggregator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

const validResponse = `{"data":[],"count":0,"sources":{"BMKG":{"available":true,"stale":false,"last_ingested_at":"2026-10-06T00:00:00Z","stale_after_seconds":15},"PVMBG":{"available":false,"stale":true,"stale_since":"2026-10-06T00:00:00Z","stale_after_seconds":15}}}`

func TestListPreservesCommittedFreshnessAndAdditiveFields(t *testing.T) {
	// Stored data remains readable during an outage; retain internal freshness metadata.
	const payload = `{"data":[{"hazard_id":"PVMBG-1","attributes":{"confidence_level":0.9}}],"count":1,"sources":{"BMKG":{"available":true,"last_ingested_at":"2026-10-05T01:00:00Z","stale":false,"stale_after_seconds":15},"PVMBG":{"available":false,"last_success_at":"2026-10-05T00:59:00Z","last_ingested_at":"2026-10-05T00:59:00Z","last_error":"outage","ingestion_error":"mapping failed","stale":true,"stale_since":"2026-10-05T01:00:00Z","stale_after_seconds":15,"future_field":"preserved"}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer upstream.Close()
	client, err := NewClient(upstream.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.List(context.Background(), nil, "freshness-test")
	expected, _ := DecodeEnvelope([]byte(payload))
	if err != nil || !reflect.DeepEqual(response, expected) {
		t.Fatalf("freshness or additive fields changed: %+v (%v)", response, err)
	}
}

func TestListDoesNotFollowRedirects(t *testing.T) {
	var redirectedCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedCalls.Add(1)
		_, _ = w.Write([]byte(validResponse))
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, time.Second, nil)
	_, err := client.List(context.Background(), nil, "redirect-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorUpstreamStatus || requestErr.Status != 302 || redirectedCalls.Load() != 0 {
		t.Fatal("Aggregator redirect followed or misclassified")
	}
}

func TestListForwardsSupportedFiltersAndCorrelationID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != hazardsPath || r.URL.Query().Get("source") != "BMKG" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("unexpected request URL: %s", r.URL.String())
		}
		if r.URL.Query().Has("unsupported") {
			t.Error("unsupported filter was forwarded")
		}
		if r.Header.Get("X-Correlation-ID") != "request-123" {
			t.Errorf("correlation ID not forwarded")
		}
		_, _ = w.Write([]byte(validResponse))
	}))
	defer upstream.Close()
	client, err := NewClient(upstream.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.List(context.Background(), url.Values{"source": {"BMKG"}, "limit": {"5"}, "unsupported": {"x"}}, "request-123")
	expected, _ := DecodeEnvelope([]byte(validResponse))
	if err != nil || !reflect.DeepEqual(response, expected) {
		t.Fatalf("unexpected result: %+v (%v)", response, err)
	}
}

func TestListClassifiesTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, 20*time.Millisecond, nil)
	_, err := client.List(context.Background(), nil, "timeout-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestListRejectsMalformedResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":0}`))
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, time.Second, nil)
	_, err := client.List(context.Background(), nil, "malformed-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorInvalidResponse {
		t.Fatalf("expected invalid response, got %v", err)
	}
}

func TestListClassifiesUpstreamStatus(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		client, _ := NewClient(upstream.URL, time.Second, nil)
		_, err := client.List(context.Background(), nil, "status-test")
		upstream.Close()
		var requestErr *RequestError
		if !errors.As(err, &requestErr) || requestErr.Kind != ErrorUpstreamStatus || requestErr.Status != status {
			t.Fatalf("status %d: unexpected error %v", status, err)
		}
	}
}

func TestListHonorsCanceledContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validResponse))
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.List(ctx, nil, "canceled-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorCanceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestListClassifiesConnectionRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	upstream.Close()
	client, err := NewClient(upstream.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background(), nil, "connection-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorTransport {
		t.Fatalf("expected transport failure, got %v", err)
	}
}

func TestListClassifiesResponseBodyTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client, err := NewClient(upstream.URL, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background(), nil, "body-timeout-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorTimeout {
		t.Fatalf("expected body timeout, got %v", err)
	}
}
